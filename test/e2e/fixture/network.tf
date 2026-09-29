# The VCN lives in the run compartment while the instance lives in the nested one, so the run
# also covers a cross-compartment reference: the nested compartment cannot be emptied without
# terminating an instance whose subnet belongs to its parent.

resource "oci_core_vcn" "main" {
  compartment_id = oci_identity_compartment.run.id
  cidr_blocks    = ["10.42.0.0/16"]
  display_name   = "${local.name_prefix}-vcn"
  dns_label      = replace(substr("e2e${var.run_id}", 0, 15), "-", "")

  freeform_tags = local.freeform_tags
}

resource "oci_core_internet_gateway" "igw" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name_prefix}-igw"
  enabled        = true

  freeform_tags = local.freeform_tags
}

# A second gateway type, because they are separate resource types in the registry and a run that
# handles one and not the other would still look clean with only an internet gateway seeded.
resource "oci_core_nat_gateway" "nat" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name_prefix}-nat"

  freeform_tags = local.freeform_tags
}

resource "oci_core_route_table" "public" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name_prefix}-rt"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.igw.id
  }

  freeform_tags = local.freeform_tags
}

# Regional, not AD-specific: OKE needs a regional subnet.
#
# This one carries the cluster's API endpoint, the service load balancers, and the standalone
# instance. It deliberately does NOT carry the node pool -- see oci_core_subnet.nodes below for
# the reason, which is not a matter of taste.
resource "oci_core_subnet" "main" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  cidr_block     = "10.42.1.0/24"
  display_name   = "${local.name_prefix}-subnet"
  dns_label      = "main"
  route_table_id = oci_core_route_table.public.id

  freeform_tags = local.freeform_tags
}

# The node pool needs a subnet of its own, and the fixture originally did not give it one on the
# reasoning that "one subnet keeps the fixture small". OCI disagrees:
#
#   400-InvalidParameter, Invalid nodeConfigDetails.placementConfigs[].subnetId:
#   The service subnets cannot be used by node pools.
#
# A subnet named in a cluster's `options.service_lb_subnet_ids` is a service subnet, and node pools
# are refused in one. So the nodes get their own -- which also lets them route the way real nodes
# do, through the NAT gateway rather than an internet gateway they have no public address for.
resource "oci_core_route_table" "private" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name_prefix}-rt-private"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_nat_gateway.nat.id
  }

  freeform_tags = local.freeform_tags
}

resource "oci_core_subnet" "nodes" {
  compartment_id             = oci_identity_compartment.run.id
  vcn_id                     = oci_core_vcn.main.id
  cidr_block                 = "10.42.2.0/24"
  display_name               = "${local.name_prefix}-subnet-nodes"
  dns_label                  = "nodes"
  route_table_id             = oci_core_route_table.private.id
  prohibit_public_ip_on_vnic = true

  freeform_tags = local.freeform_tags
}

# Added to satisfy CKV2_OCI_3, and worth having on its own account: NetworkSecurityGroup is a
# registered resource type the fixture did not otherwise seed.
#
# It also turned out to be load-bearing. The cluster's API endpoint and the node pool are both
# members of this one group, so rules whose source is the group itself are exactly "these things
# may talk to each other" -- see the rules below for why the fixture cannot do without them.
resource "oci_core_network_security_group" "main" {
  compartment_id = oci_identity_compartment.run.id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name_prefix}-nsg"

  freeform_tags = local.freeform_tags
}

# An NSG with no rules was the fixture's state until a node pool actually tried to boot, and then:
#
#   Work Request error ... entity: nodepool, action: CREATED.
#   Message: 1 nodes(s) register timeout. First, confirm that network prerequisites have been met.
#
# after twenty-two minutes. The nodes were created; they could not reach the cluster's private API
# endpoint. NSGs and security lists are unioned and both are allow-only, so the effective rules
# were the VCN's default security list -- all egress, and ingress on TCP 22 only. Nothing let a
# node reach the endpoint on 6443, so nothing ever registered.
#
# Source and destination are this same NSG rather than CIDR blocks, because both ends of the
# conversation are in it: it says "the members of this group may talk to each other" without
# naming a subnet, so it keeps working if the addressing changes.

resource "oci_core_network_security_group_security_rule" "intra_group" {
  # checkov:skip=CKV2_OCI_2: the check passes only a rule that is INGRESS, from something other
  # than 0.0.0.0/0, and not protocol "all". This one is the first two but not the third, so it
  # fails -- and literally that is true, RDP between the two members is permitted. Neither member
  # runs RDP, the source is the group itself rather than the internet, and both VNICs exist for
  # about forty minutes before the tool under test deletes them. Narrowing to a port list would
  # trade that for a list to keep in step with OKE's requirements, which is the failure mode this
  # fixture already hit once.
  network_security_group_id = oci_core_network_security_group.main.id
  direction                 = "INGRESS"
  # Everything, deliberately, and only within the group: kubelet to the API server on 6443, the
  # control plane back to kubelet on 10250, OKE's own 12250, and node-to-node for the CNI.
  protocol    = "all"
  source_type = "NETWORK_SECURITY_GROUP"
  source      = oci_core_network_security_group.main.id
  description = "Cluster endpoint and nodes may talk to each other"

  # CKV_OCI_21, and satisfied rather than skipped because the recommendation is sound: a stateless
  # rule keeps the connection out of the VNIC's tracking table.
  #
  # It is safe here because the rule is symmetric. Both ends of every intra-group conversation are
  # members of this group, so a reply travelling the other way matches this same rule as ingress at
  # the other VNIC, and leaves through the egress-to-anywhere rule below. Nothing depends on
  # connection state being remembered.
  stateless = true
}

resource "oci_core_network_security_group_security_rule" "egress_all" {
  # checkov:skip=CKV2_OCI_2: an egress rule cannot expose an RDP listener, and this check cannot
  # say so -- its only passing branch requires direction = INGRESS, so every egress rule in every
  # NSG fails it by construction. Read the check's own definition before treating this as a
  # judgement call.
  #
  # Deliberately left stateful, unlike the two ingress rules: this is the path to the internet
  # through the NAT gateway for image pulls, and return traffic from the internet is allowed by
  # connection state and by nothing else. A stateless rule here would need a matching ingress from
  # 0.0.0.0/0, which is precisely what should not exist.
  network_security_group_id = oci_core_network_security_group.main.id
  direction                 = "EGRESS"
  protocol                  = "all"
  destination_type          = "CIDR_BLOCK"
  destination               = "0.0.0.0/0"
  # The VCN's default security list already allows this. Stated here anyway so the fixture does
  # not depend on the contents of a security list it never wrote: nodes need egress through the
  # NAT gateway to pull images and to reach OCI services.
  description = "Egress for image pulls and OCI services, via the NAT gateway"
}

resource "oci_core_network_security_group_security_rule" "icmp_path_mtu" {
  # checkov:skip=CKV2_OCI_2: protocol 1 is ICMP and cannot carry TCP 3389. The check fails this
  # rule because its passing branch also requires source != 0.0.0.0/0, and path MTU discovery
  # messages arrive from wherever the constricted hop is -- that is the whole mechanism.
  network_security_group_id = oci_core_network_security_group.main.id
  direction                 = "INGRESS"
  protocol                  = "1" # ICMP
  source_type               = "CIDR_BLOCK"
  source                    = "0.0.0.0/0"
  description               = "Path MTU discovery, which OKE's network prerequisites call for"

  # CKV_OCI_21 again, and free here: an ICMP type 3 code 4 is a notification, not a conversation,
  # so there is no return traffic for connection state to have tracked.
  stateless = true

  icmp_options {
    type = 3
    code = 4
  }
}
