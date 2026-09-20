package auth

import (
	"fmt"
	"net/smtp"
)

// Mailer sends magic-link emails via plain SMTP. There's no third-party
// email API client here — Go's standard library already speaks SMTP
// (net/smtp), and Mailhog (local dev — see docker-compose.yml) or most
// managed SMTP relays (production — see DEPLOY.md) both just want an
// SMTP connection, so there's nothing a dependency would add for a single
// outgoing email per sign-in.
type Mailer struct {
	Host string
	Port int
	From string
}

// SendMagicLink emails a one-time sign-in link to to. link should already
// be the full URL (see internal/web's handler for how it's built from
// config.PublicBaseURL).
func (m Mailer) SendMagicLink(to, link string) error {
	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)
	body := fmt.Sprintf(
		"Sign in to MRF Sentinel by clicking the link below.\r\n\r\n%s\r\n\r\nThis link expires in 15 minutes and can only be used once. If you didn't request it, you can safely ignore this email.\r\n",
		link,
	)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Sign in to MRF Sentinel\r\n\r\n%s", m.From, to, body)

	// smtp.SendMail's auth parameter is nil: Mailhog and many local/dev
	// SMTP relays accept unauthenticated mail on purpose, and that's the
	// only target this app talks to out of the box. A real SMTP provider
	// in production needs credentials wired in here via smtp.PlainAuth —
	// flagged explicitly in DEPLOY.md rather than left as a silent gap.
	if err := smtp.SendMail(addr, nil, m.From, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("auth: sending magic-link email to %s: %w", to, err)
	}
	return nil
}
