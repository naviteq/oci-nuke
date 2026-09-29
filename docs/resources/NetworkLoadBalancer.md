# NetworkLoadBalancer

- Scope: compartment
- DependsOn: none

## Properties

- `backend_sets`
- `compartment_id`
- `id`
- `lifecycle_state`
- `name`
- `time_created`

## Notes

Network load balancers are enumerated unconditionally over the compartment, with no relationship
to any OKE cluster -- one created as a side effect of a Kubernetes `LoadBalancer`-type Service is
listed and deleted exactly like one created any other way (RES-11). This is proven by
`resources_test/oke_independent_enumeration_test.go`'s `TestOKEIndependentEnumeration`.
