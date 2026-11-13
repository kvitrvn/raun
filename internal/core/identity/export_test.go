package identity

// Held is the number of identities whose lock s keeps.
func Held(s *TokenService) int { return s.held() }
