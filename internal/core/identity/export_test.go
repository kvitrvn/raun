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

// Waiters is the number of callers that joined the question in flight about
// account and repository on p.
func Waiters(p *PermissionService, account string, repo Repository) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.inflight[permissionKey{account: account, forge: repo.Forge, repository: repo.ID}]; ok {
		return c.waiters
	}
	return 0
}
