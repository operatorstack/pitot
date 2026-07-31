package runtime

// Capability identifies a shipped runtime behavior supervised by repository CI.
type Capability string

const (
	CapabilityActionControl Capability = "action_control"
	// CapabilityHookControl is retained as a source-compatible alias.
	CapabilityHookControl      Capability = CapabilityActionControl
	CapabilityConsumerDelivery Capability = "consumer_delivery"
	CapabilityExplicitRequest  Capability = "explicit_request"
)

// Capabilities returns the canonical ordered shipped runtime inventory.
func Capabilities() []Capability {
	return []Capability{CapabilityActionControl, CapabilityConsumerDelivery, CapabilityExplicitRequest}
}
