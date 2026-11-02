package service

// CapacityHooks are optional observation points inside Provider slot admission. They exist
// so failure tests can hold one admission at an exact point while another races it,
// without sleeping.
type CapacityHooks struct {
	// AfterProviderLock runs inside the claim transaction right after the Provider row
	// lock was taken, before the held count is read. A second admission of the same
	// Provider is blocked on that row lock until this transaction ends.
	AfterProviderLock func(nodeRunID string)
}
