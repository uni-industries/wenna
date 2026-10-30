package config

import "log/slog"

const redacted = "[redacted]"

// Secret holds a credential. fmt verbs and slog print a placeholder; JSON
// sees no exported field. Only Reveal returns the value.
type Secret struct{ v string }

var (
	_ slog.LogValuer = Secret{}
	_ slog.LogValuer = Serve{}
)

// Reveal returns the credential. Call it only where the value is used.
func (s Secret) Reveal() string { return s.v }

func (Secret) String() string       { return redacted }
func (Secret) GoString() string     { return redacted }
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// LogValue redacts on the outermost struct, since slog does not recurse
// looking for a LogValuer. The OAuth owners are personal data: their presence
// is logged, never their value.
func (c Serve) LogValue() slog.Value {
	access := []slog.Attr{slog.Bool("on", c.Access != nil)}
	if a := c.Access; a != nil {
		access = append(access,
			slog.String("team_domain", a.TeamDomain),
			slog.String("aud", a.Audience),
			slog.Bool("owner_email_set", a.OwnerEmail != ""))
	}
	workos := []slog.Attr{slog.Bool("on", c.WorkOS != nil)}
	if w := c.WorkOS; w != nil {
		workos = append(workos,
			slog.String("issuer", w.Issuer),
			slog.String("resource", w.Resource),
			slog.Bool("owner_id_set", w.OwnerSubject != ""))
	}
	return slog.GroupValue(
		slog.String("db", c.DBPath),
		slog.String("addr", c.Addr),
		slog.String("token", redacted),
		slog.String("public_url", c.PublicURL),
		slog.Attr{Key: "access", Value: slog.GroupValue(access...)},
		slog.Attr{Key: "workos", Value: slog.GroupValue(workos...)},
	)
}
