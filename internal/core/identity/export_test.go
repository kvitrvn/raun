package identity

// Held is the number of identities whose lock s keeps.
func Held(s *TokenService) int { return s.held() }

// Counts is the number of invalidation counts and questions in flight p
// keeps.
func Counts(p *PermissionService) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accountGen) + len(p.identityGen) + len(p.keyGen) + len(p.inflight)
}
