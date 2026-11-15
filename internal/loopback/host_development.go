//go:build devweb

package loopback

// developmentHost is the second name a development build answers at, so a
// development Vult on the same Mac never shares the installed Vult's name,
// certificate, sign-in cookies or passkeys.
const developmentHost = "dev." + Host
