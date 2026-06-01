package oauth

import "encoding/base32"

// base32Alphabet returns a Crockford-like base32 encoder without padding.
// Using stdlib base32.StdEncoding.WithPadding(base32.NoPadding) to keep
// alphabetic + numeric characters that survive WhatsApp/SMS rendering well.
func base32Alphabet() *base32.Encoding {
	return base32.StdEncoding.WithPadding(base32.NoPadding)
}
