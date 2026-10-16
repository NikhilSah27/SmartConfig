package redact

import (
	"strings"
	"testing"
)

// The tests below close the gaps the chunk K review's mutations found
// (K12): each pins one rule that no test held. Their made-up values hold
// "sekrit", so a leak names its input (see shown).

// The key names: a localised key is judged by its base name, PINs after
// any separator, and the spellings of passwords and private keys.
func TestGapKeyNames(t *testing.T) {
	for k, want := range map[string]bool{
		"pin[de]": true, "psk-flags[de]": false, // the locale is not part of the name
		"sim_pin": true, "gsm-pin": true, // a PIN after "_" or "-"
		"passwd": true, "PassPhrase": true, // cloud-init's passwd; case does not matter
		"ssl_private_key": true, "tls-private-key": true, "key-private": true,
		"client_secret": true, "pinned_cert": false,
	} {
		if secretKey(k) != want {
			t.Errorf("secretKey(%q) = %v", k, !want)
		}
	}
	in := "[gsm]\npin[de]=4711\npin-flags[de]=0\n"
	if got := hide(t, "/etc/x.nmconnection", in); got != "[gsm]\npin[de]=<hidden>\npin-flags[de]=0\n" {
		t.Errorf("localised keys: %q", got)
	}
}

// A blank line inside a block scalar holds nothing and does not end it:
// the lines after it are still the secret's.
func TestGapBlockScalarBlankLine(t *testing.T) {
	in := wifiHead + "        \"Home\":\n          password: |\n            sekrit1\n\n            sekrit2\n  \n            sekrit3\n          mode: ap\n"
	want := wifiHead + "        \"Home\":\n          password: |\n            <hidden>\n\n            <hidden>\n  \n            <hidden>\n          mode: ap\n"
	if got := hide(t, "/etc/netplan/x.yaml", in); got != want {
		t.Errorf("got:\n%s", got)
	}
}

// A block scalar's indicator, "|" or ">" with its chomping, is not the
// secret: it stays, and the lines under it are hidden.
func TestGapBlockIndicators(t *testing.T) {
	for _, ind := range []string{">-", ">", "|-", "|+"} {
		in := "          password: " + ind + "\n            sekrit1\n          mode: ap\n"
		want := "          password: " + ind + "\n            <hidden>\n          mode: ap\n"
		if got := hide(t, "/etc/netplan/x.yaml", in); got != want {
			t.Errorf("%s: %q", ind, got)
		}
	}
}

// YAML's quoting: a backslash escapes a double quote, so the value runs to
// the real closing quote, and a comment's "#" needs a blank before it, so
// a "#" inside a value is part of it.
func TestGapYAMLQuotesAndComments(t *testing.T) {
	for in, want := range map[string]string{
		"password: \"sek\\\"rit1\" # c\n":                      "password: <hidden> # c\n",
		"password: sek#rit1\n":                                 "password: <hidden>\n",
		"password: sekrit1 #rit2\n":                            "password: <hidden> #rit2\n",
		"\"password\": sekrit1\n":                              "\"password\": <hidden>\n",
		"'password': 'sekrit1'\n":                              "'password': <hidden>\n",
		"{key-management: psk, password: sekrit1, mode: ap}\n": "{key-management: psk, password: <hidden>, mode: ap}\n",
		"- [{password: sekrit1}, {mode: ap}]\n":                "- [{password: <hidden>}, {mode: ap}]\n",
	} {
		if got := hide(t, "/etc/netplan/x.yaml", in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A secret value's own text is not read for keys: a password may hold
// "psk: '", and what follows the closed value is shown again.
func TestGapValueNotRead(t *testing.T) {
	in := "          password: \"my psk: 'q\"\n          mode: ap\n"
	if got := hide(t, "/etc/netplan/x.yaml", in); got != "          password: <hidden>\n          mode: ap\n" {
		t.Errorf("got %q", got)
	}
}

// A flow mapping whose secret value is on a later line: a blank line in
// between does not take its place, and an escaped quote or a "#" with no
// blank before it earlier on the line is no comment, so the key is open.
func TestGapFlowNext(t *testing.T) {
	for _, in := range []string{
		"        \"Home\": {password:\n\n sekrit1}\n",
		"        \"Home\": {note: \"q\\\" #\", password:\n sekrit1}\n",
		"        \"Home\": {note: x#y, password:\n sekrit1}\n",
	} {
		shown(t, in, hide(t, "/etc/netplan/x.yaml", wifiHead+in))
	}
	// A flow key in a comment leaves nothing open; nor does an explicit
	// key that names no secret.
	for _, in := range []string{"# {password:\n      ssid: Home\n", "      ? ssid\n      : Home\n"} {
		if got := hide(t, "/etc/netplan/x.yaml", in); got != in {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// The edges of shadow's rule: every "!" that locks a password is kept; a
// line of blanks of any kind is no account; a copy is found by any name
// that holds shadow's, before or after.
func TestGapShadow(t *testing.T) {
	in := "bob:!!$6$s$sekrit1:19000::::::\n\v\n \f \nann:sekritDES12:1::::::\n"
	want := "bob:!!<hidden>:19000::::::\n\v\n \f \nann:<hidden>:1::::::\n"
	for _, p := range []string{"/etc/shadow", "/etc/old-gshadow", "/etc/shadow.bkp", "/root/gshadow-2024"} {
		got, n := File(p, []byte(in), Plain)
		if string(got) != want || n != 2 {
			t.Errorf("%s: %q, %d", p, got, n)
		}
	}
}

// An empty secret value has nothing to hide: it is shown as it is, and not
// counted. A blank after a hidden value is kept in a "key value" line, as
// in a key=value one.
func TestGapEmptyValues(t *testing.T) {
	in := "[wifi-security]\npassword=\nwep-key0= \n"
	if got, n := File("/etc/x.nmconnection", []byte(in), Plain); string(got) != in || n != 0 {
		t.Errorf("empty: %q, %d", got, n)
	}
	if got := hide(t, "/etc/msmtprc", "password sekrit1  \n"); got != "password <hidden>  \n" {
		t.Errorf("key value: %q", got)
	}
	// A private key with nothing in it on one line.
	bare := "x " + begin("", "") + " -----END PRIVATE KEY----- y\n"
	if got, n := File("/etc/x.conf", []byte(bare), Plain); string(got) != bare || n != 0 {
		t.Errorf("empty key: %q, %d", got, n)
	}
}

// Only a real END line ends a private key: a body line that only begins
// like one is hidden, and so are the lines after it.
func TestGapPEMEnd(t *testing.T) {
	in := begin("", "") + "\nsekrit1\n-----ENDsekrit2\nsekrit3\n-----END PRIVATE KEY-----\nafter\n"
	want := begin("", "") + "\n<hidden>\n<hidden>\n<hidden>\n-----END PRIVATE KEY-----\nafter\n"
	if got := hide(t, "/etc/x.conf", in); got != want {
		t.Errorf("got:\n%s", got)
	}
	// One that only looks closed on its BEGIN line stays open.
	got := hide(t, "/etc/x.conf", begin("", "")+" -----ENDx\nsekrit2\n-----END PRIVATE KEY-----\n")
	if !strings.HasSuffix(got, "\n<hidden>\n-----END PRIVATE KEY-----\n") {
		t.Errorf("open: %q", got)
	}
}

// Every OpenVPN inline block that holds a key, a secret or a password.
func TestGapOpenVPNBlocks(t *testing.T) {
	for _, tag := range []string{"key", "secret", "tls-auth", "tls-crypt", "tls-crypt-v2", "auth-user-pass", "http-proxy-user-pass", "pkcs12"} {
		in := "<" + tag + ">\nsekrit1\n</" + tag + ">\nverb 3\n"
		if got := hide(t, "/etc/openvpn/x.conf", in); got != "<"+tag+">\n<hidden>\n</"+tag+">\nverb 3\n" {
			t.Errorf("%s: %q", tag, got)
		}
	}
}

// A validator's line: a value of 8 bytes or more is hidden wherever it
// stands, one under 8 only where it stands alone; a value is also found
// with the blanks inside its quotes trimmed, and at every place it stands.
func TestGapLineValues(t *testing.T) {
	vs := Values("/etc/x.nmconnection", []byte("[802-1x]\npassword=sekrit7\npin=sekrit12\n"), false)
	if got := Line("xsekrit7y xsekrit12y", vs, Plain); got != "xsekrit7y x<hidden>y" {
		t.Errorf("limit: %q", got)
	}
	if got := Line("sekrit7, sekrit7 and sekrit12/sekrit12", vs, Plain); got != "<hidden>, <hidden> and <hidden>/<hidden>" {
		t.Errorf("every place: %q", got)
	}
	vs = Values("/etc/netplan/x.yaml", []byte("password: \"  sekrit1 \"\n"), false)
	shown(t, "trimmed", Line("invalid password sekrit1.", vs, Plain))
}

// A YAML line that is only a flow sequence, "[...]", is no keyfile
// section: the YAML rule reads it (cloud-init's users in flow style;
// /etc/cloud is a secret directory). Found by the K12 tests.
func TestGapFlowSequenceLine(t *testing.T) {
	for p, in := range map[string]string{
		"/etc/cloud/cloud.cfg": "users:\n  [{name: me, plain_text_passwd: sekrit1}]\n",
		"/etc/netplan/x.yaml":  "      auth:\n        [{password: sekrit1}]\n        [password: sekrit2]\n        [{\"password\":\"sekrit3\"}]\n",
	} {
		shown(t, p, hide(t, p, in))
	}
	// A keyfile's section names are kept as they are.
	in := "[vpn-secrets]\nk=sekrit\n[wireguard-peer.ab+c=]\nendpoint=h:1\n"
	if got := hide(t, "/etc/x.nmconnection", in); got != "[vpn-secrets]\nk=<hidden>\n[wireguard-peer.ab+c=]\nendpoint=h:1\n" {
		t.Errorf("sections: %q", got)
	}
}

// Text after a private key's BEGIN header on its line is the key's body.
// Found by the K12 tests.
func TestGapPEMBeginLineTail(t *testing.T) {
	in := begin("", "") + "sekrit1\nsekrit2\n-----END PRIVATE KEY-----\n"
	if got := hide(t, "/etc/x.conf", in); got != begin("", "")+"<hidden>\n<hidden>\n-----END PRIVATE KEY-----\n" {
		t.Errorf("%q", got)
	}
}
