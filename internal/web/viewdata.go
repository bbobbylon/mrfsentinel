package web

import "github.com/bobbylon127/mrfsentinel/internal/store"

// baseData is embedded (anonymously) in every page's data struct so
// layout.html can always safely reference .User via Go template's field
// promotion, whether or not the specific page below it is one that
// actually has a signed-in user. User is a pointer specifically so
// layout.html's {{with .User}} works correctly: text/template's `with`
// treats a nil pointer as "empty" and skips its block, but would treat
// even a zero-value store.User struct as "present" — nil is what makes
// "no one is signed in yet" (the login and check-your-email pages) render
// correctly instead of showing a blank sign-out form.
type baseData struct {
	User *store.User
}

// authedData wraps a signed-in user in the baseData every page's data
// struct embeds. Taking store.User by value and returning a pointer to that
// copy is deliberate: it gives layout.html's {{with .User}} a non-nil
// pointer to branch on (see baseData above) without handing a template a
// pointer into the caller's own variable.
func authedData(user store.User) baseData {
	return baseData{User: &user}
}
