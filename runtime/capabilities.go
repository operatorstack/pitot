package runtime

// Capability identifies a shipped runtime behavior supervised by repository CI.
type Capability string

const (
	CapabilityHookControl      Capability = "hook_control"
	CapabilityConsumerDelivery Capability = "consumer_delivery"
	CapabilityExplicitRequest  Capability = "explicit_request"
)

// Capabilities returns the canonical ordered shipped runtime inventory.
func Capabilities() []Capability {
	return []Capability{CapabilityHookControl, CapabilityConsumerDelivery, CapabilityExplicitRequest}
}
