package broker

// NewWithProviders builds a Broker over an explicit provider chain, for tests
// that inject fault-injecting fakes (the production New wires the real GLM chain).
func NewWithProviders(providers ...Provider) *Broker {
	return &Broker{chain: providers}
}
