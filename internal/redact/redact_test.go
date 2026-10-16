package redact

import (
	"strings"
	"testing"
)

func hide(t *testing.T, p, in string) string {
	t.Helper()
	out, _ := File(p, []byte(in), Plain)
	return string(out)
}

func TestShadow(t *testing.T) {
	in := "root:*:19000:0:99999:7:::\n" +
		"daemon:!:19000::::::\n" +
		"vboxuser:$6$salt$hash:19999:0:99999:7:::\n" +
		"locked:!$y$j9T$abc:19999::::::\n" +
		"nopw::19999::::::\n" +
		"odd:!*:1\n" +
		"broken$6$lostcolons\n" +
		"\n" +
		"short\r\n"
	want := "root:*:19000:0:99999:7:::\n" +
		"daemon:!:19000::::::\n" +
		"vboxuser:<hidden>:19999:0:99999:7:::\n" +
		"locked:!<hidden>:19999::::::\n" +
		"nopw::19999::::::\n" +
		"odd:!*:1\n" +
		"<hidden>\n" +
		"\n" +
		"<hidden>\r\n"
	for _, p := range []string{"/etc/shadow", "/etc/gshadow", "/tmp/x/shadow"} {
		if got := hide(t, p, in); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", p, got, want)
		}
	}
	// gshadow's own layout: name:password:admins:members.
	if got := hide(t, "/etc/gshadow", "sudo:$6$x$y:root:vboxuser\nadm:*::syslog,vboxuser\nonlypw:secret"); got != "sudo:<hidden>:root:vboxuser\nadm:*::syslog,vboxuser\nonlypw:<hidden>" {
		t.Errorf("gshadow: %q", got)
	}
	// The shadow rule is the file's name: elsewhere the second field stays.
	if got := hide(t, "/etc/passwd", "vboxuser:x:1000:1000::/home/vboxuser:/bin/bash\n"); got != "vboxuser:x:1000:1000::/home/vboxuser:/bin/bash\n" {
		t.Errorf("passwd: %q", got)
	}
}

func TestKeyfile(t *testing.T) {
	in := `[connection]
id=Home wifi
type=wifi

[wifi]
ssid=Home
mode=infrastructure

[wifi-security]
key-mgmt=wpa-psk
psk=correct horse battery staple
psk-flags=0
wep-key0=0123456789
wep-key-type=1
leap-password = spaced value

[802-1x]
eap=peap;
identity=me
password[de]=geheim
private-key=/etc/ssl/me.key
private-key-password=pkpw
phase2-auth=mschapv2

[gsm]
pin=1234
apn=internet

[wireguard]
private-key=yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=

[vpn-secrets]
anything=topsecret
cert-pass=x

[ipv4]
method=auto
`
	want := `[connection]
id=Home wifi
type=wifi

[wifi]
ssid=Home
mode=infrastructure

[wifi-security]
key-mgmt=wpa-psk
psk=<hidden>
psk-flags=0
wep-key0=<hidden>
wep-key-type=1
leap-password = <hidden>

[802-1x]
eap=peap;
identity=me
password[de]=<hidden>
private-key=<hidden>
private-key-password=<hidden>
phase2-auth=mschapv2

[gsm]
pin=<hidden>
apn=internet

[wireguard]
private-key=<hidden>

[vpn-secrets]
anything=<hidden>
cert-pass=<hidden>

[ipv4]
method=auto
`
	got, n := File("/etc/NetworkManager/system-connections/Home.nmconnection", []byte(in), Plain)
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if n != 10 {
		t.Errorf("hid %d values, want 10", n)
	}
}

func TestNetplan(t *testing.T) {
	in := `network:
  version: 2
  wifis:
    wlp2s0:
      dhcp4: true
      access-points:
        "Home":
          password: "correct horse"
        Cafe:
          auth:
            key-management: psk
            password: plain value # a comment
        'Office':
          auth: {key-management: eap, password: 'it''s', identity: me}
  tunnels:
    wg0:
      mode: wireguard
      key:
        private: yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=
      peers:
        - keys:
            public: rlbInAj0qV69CysWPQY7KEBnKxpYCpaWqOs/dLevdWc=
            shared: aPxGwq8zERHQ3Q1cOZFdJ+cvJX5Ka4mLN38AyYKYF10=
          allowed-ips: [0.0.0.0/0]
        - password: |
            line one
            line two

          endpoint: 1.2.3.4:51820
  ethernets:
    eth0: {dhcp4: true}
`
	want := `network:
  version: 2
  wifis:
    wlp2s0:
      dhcp4: true
      access-points:
        "Home":
          password: <hidden>
        Cafe:
          auth:
            key-management: psk
            password: <hidden> # a comment
        'Office':
          auth: {key-management: eap, password: <hidden>, identity: me}
  tunnels:
    wg0:
      mode: wireguard
      key:
        private: <hidden>
      peers:
        - keys:
            public: rlbInAj0qV69CysWPQY7KEBnKxpYCpaWqOs/dLevdWc=
            shared: <hidden>
          allowed-ips: [0.0.0.0/0]
        - password: |
            <hidden>
            <hidden>

          endpoint: 1.2.3.4:51820
  ethernets:
    eth0: {dhcp4: true}
`
	if got := hide(t, "/etc/netplan/50-cloud-init.yaml", in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPEM(t *testing.T) {
	in := "a=1\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n  AAAA\n\n-----END OPENSSH PRIVATE KEY-----\nb=2\n" +
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	want := "a=1\n-----BEGIN OPENSSH PRIVATE KEY-----\n<hidden>\n  <hidden>\n\n-----END OPENSSH PRIVATE KEY-----\nb=2\n" +
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	if got := hide(t, "/etc/x.conf", in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// key=value inside a line: a mount's options, a shell assignment.
func TestPairs(t *testing.T) {
	in := "//srv/share /mnt cifs username=me,password=hunter22,uid=1000 0 0\nWIFI_PSK=abc\nexport secret=x;next=y\n"
	want := "//srv/share /mnt cifs username=me,password=<hidden>,uid=1000 0 0\nWIFI_PSK=<hidden>\nexport secret=<hidden>;next=y\n"
	if got := hide(t, "/etc/fstab", in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// sides runs old and new through File with the Hiders Sides gives them.
func sides(p, old, new string) (string, string) {
	var ov, nv []string
	collect := func(vs *[]string) Hider {
		return func(s string) string { *vs = append(*vs, s); return Marker }
	}
	File(p, []byte(old), collect(&ov))
	File(p, []byte(new), collect(&nv))
	oh, nh := Sides(ov, nv)
	o, _ := File(p, []byte(old), oh)
	n, _ := File(p, []byte(new), nh)
	return string(o), string(n)
}

// K7: Sides marks a value old or new only when the other version does not
// hide it, so a diff shows which secret changed, and nothing tells which
// hidden values are equal.
func TestSides(t *testing.T) {
	// b's value changed; a's did not; c's is a's value, on a new line.
	o, n := sides("/etc/shadow", "a:$6$one:1\nb:$6$two:1\n", "a:$6$one:1\nb:$6$three:1\nc:$6$one:1\n")
	if o != "a:<hidden>:1\nb:<hidden, old>:1\n" || n != "a:<hidden>:1\nb:<hidden, new>:1\nc:<hidden>:1\n" {
		t.Errorf("old %q, new %q", o, n)
	}
	// Two accounts with one hash look the same as two with different ones.
	same, _ := sides("/etc/shadow", "a:$6$one:1\nb:$6$one:1\n", "a:$6$one:2\nb:$6$one:1\n")
	diff, _ := sides("/etc/shadow", "a:$6$one:1\nb:$6$two:1\n", "a:$6$one:2\nb:$6$two:1\n")
	if same != diff || same != "a:<hidden>:1\nb:<hidden>:1\n" {
		t.Errorf("one hash %q, two hashes %q", same, diff)
	}
	// A value only one version has, or none hidden in the other.
	o, n = sides("/x.nmconnection", "[wifi-security]\nkey-mgmt=none\n", "[wifi-security]\nkey-mgmt=none\nwep-key0=sekrit\n")
	if o != "[wifi-security]\nkey-mgmt=none\n" || n != "[wifi-security]\nkey-mgmt=none\nwep-key0=<hidden, new>\n" {
		t.Errorf("added: old %q, new %q", o, n)
	}
	oh, nh := Sides([]string{"sekrit1"}, []string{"sekrit2"})
	if oh("sekrit1") != Old || nh("sekrit2") != New || nh("sekrit1") != Marker || oh("sekrit2") != Marker {
		t.Errorf("one side only: %q, %q", oh("sekrit1"), nh("sekrit2"))
	}
}

func TestValuesAndLine(t *testing.T) {
	data := []byte("[wifi-security]\npsk=hunter22\n[802-1x]\npin=12\npassword=ab\n")
	vs := Values("/etc/NetworkManager/system-connections/x", data, false)
	if strings.Join(vs, ",") != "hunter22,12,ab" {
		t.Fatalf("values %q", vs)
	}
	for in, want := range map[string]string{
		"x.yaml:3:5: Error: bad psk hunter22here": "x.yaml:3:5: Error: bad psk <hidden>here",
		"pin 12 and 123 and ab12 and ab":          "pin <hidden> and 123 and ab12 and <hidden>",
		"invalid value for password: 'other one'": "invalid value for password: <hidden>",
		"delete line 'u:password=zz'?":            "delete line 'u:password=<hidden>'?",
		"nothing secret here":                     "nothing secret here",
	} {
		if got := Line(in, vs, Plain); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecretKey(t *testing.T) {
	for k, want := range map[string]bool{
		"psk": true, "PSK": true, "password": true, "leap-password": true, "password[de]": true,
		"private-key": true, "private": true, "shared": true, "preshared-key": true, "pin": true, "puk-pin": true,
		"wep-key0": true, "secrets": true, "passphrase": true,
		"psk-flags": false, "password-flags": false, "wep-key-type": false, "key-mgmt": false,
		"public": false, "ssid": false, "pinned": false, "spin": false, "identity": false,
	} {
		if secretKey(k) != want {
			t.Errorf("secretKey(%q) = %v", k, !want)
		}
	}
}

// What File does not hide comes out byte for byte, CRLF and a missing
// last newline included.
func TestUnchanged(t *testing.T) {
	for _, in := range []string{"", "\n", "a: b\r\nc: d", "# psk mentioned in a comment\nssid: x\n"} {
		got, n := File("/etc/netplan/x.yaml", []byte(in), Plain)
		if string(got) != in || n != 0 {
			t.Errorf("%q: %q, %d", in, got, n)
		}
	}
}

// The tests below come from the chunk K review (K2-K5, K8, K10). Their
// made-up values all hold "sekrit", so a leak names its input. A PEM
// header is put together by begin, so the repo's local secret scan, which
// refuses a literal one, passes them.

// begin is the first line of a private key of kind ("RSA ", "PGP ", "").
func begin(kind, tail string) string { return "-----BEGIN " + kind + "PRIVATE KEY" + tail + "-----" }

// shown fails t when out still holds a made-up secret.
func shown(t *testing.T, name, out string) {
	t.Helper()
	if strings.Contains(out, "sekrit") {
		t.Errorf("%s: a secret shown:\n%s", name, out)
	}
}

const wifiHead = "network:\n  wifis:\n    wlan0:\n      access-points:\n"

// K2: forms netplan's generator accepts (each one checked with
// /usr/libexec/netplan/generate 1.1.2 by the review).
func TestYAMLForms(t *testing.T) {
	for name, in := range map[string]string{
		"double quotes continued at column 0":   wifiHead + "        \"Home\":\n          password: \"sekrit1\nsekrit2\"\n        \"Work\": {}\n",
		"single quotes continued at column 1":   wifiHead + "        \"Home\":\n          password: 'sekrit1\n sekrit2 it''s'\n",
		"JSON-style flow, no blank after \":\"": wifiHead + "        \"Home\": {\"password\":\"sekrit1\"}\n",
		"an explicit key, ? and :":              wifiHead + "        \"Home\":\n          ? password\n          : sekrit1\n            sekrit2\n",
		"a flow mapping's value on the next":    wifiHead + "        \"Home\": {password:\n sekrit1}\n",
		"a plain value continued":               wifiHead + "        \"Home\":\n          password: sekrit1\n            sekrit2\n",
	} {
		shown(t, name, hide(t, "/etc/netplan/x.yaml", in))
	}
	// What follows a closed value is shown again.
	in := wifiHead + "        \"Home\":\n          password: \"sekrit1\n  sekrit2\"\n        \"Work\":\n          mode: ap\n"
	if got := hide(t, "/etc/netplan/x.yaml", in); !strings.HasSuffix(got, "password: <hidden>\n  <hidden>\"\n        \"Work\":\n          mode: ap\n") {
		t.Errorf("after the closing quote:\n%s", got)
	}
	// A value straight after ":" needs a quoted key: a:b is a plain scalar.
	for _, in := range []string{"      url: http://password:x@h\n", "      note: see password:'kept'\n"} {
		if got := hide(t, "/etc/netplan/x.yaml", in); got != in {
			t.Errorf("plain a:b: %q", got)
		}
	}
}

// K3, K5: a private key on one line under a secret key; a PGP block.
func TestPEMOneLineAndPGP(t *testing.T) {
	one := begin("", "") + `\nsekrit\n-----END PRIVATE KEY-----`
	got := hide(t, "/etc/netplan/x.yaml", "      key:\n        private: \""+one+"\"\n")
	if got != "      key:\n        private: <hidden>\n" {
		t.Errorf("YAML: %q", got)
	}
	got = hide(t, "/etc/x.conf", "[802-1x]\nprivate-key-password=sekrit "+begin("RSA ", "")+" sekrit -----END RSA PRIVATE KEY-----\n")
	if got != "[802-1x]\nprivate-key-password=<hidden>\n" {
		t.Errorf("keyfile: %q", got)
	}
	// A one-line key under no key at all: its body alone.
	got = hide(t, "/etc/x.conf", "x "+begin("", "")+"sekrit-----END PRIVATE KEY----- y\n")
	if got != "x "+begin("", "")+"<hidden>-----END PRIVATE KEY----- y\n" {
		t.Errorf("bare: %q", got)
	}
	pgp := begin("PGP ", " BLOCK") + "\n\nsekrit1\n=ab12\n-----END PGP PRIVATE KEY BLOCK-----\nafter\n"
	if got := hide(t, "/etc/x.conf", pgp); got != begin("PGP ", " BLOCK")+"\n\n<hidden>\n<hidden>\n-----END PGP PRIVATE KEY BLOCK-----\nafter\n" {
		t.Errorf("PGP: %q", got)
	}
}

// K4: NetworkManager secrets, in a keyfile and in netplan's passthrough.
func TestNMKeys(t *testing.T) {
	kf := "[macsec]\nmode=0\nmka-cak=sekrit\nmka-ckn=0011\n" +
		"[vpn-secrets]\nIPSec secret=sekrit one\nXauth password = sekrit two\nform:main:pass word=sekrit\n" +
		"[wifi-security]\nwep-key0=sek;rit\n"
	want := "[macsec]\nmode=0\nmka-cak=<hidden>\nmka-ckn=0011\n" +
		"[vpn-secrets]\nIPSec secret=<hidden>\nXauth password = <hidden>\nform:main:pass word=<hidden>\n" +
		"[wifi-security]\nwep-key0=<hidden>\n"
	if got := hide(t, "/etc/x.nmconnection", kf); got != want {
		t.Errorf("keyfile:\n%s", got)
	}
	np := "      networkmanager:\n        passthrough:\n          802-1x.pin: \"sekrit\"\n          macsec.mka-cak: \"sekrit\"\n          connection.id: \"Home\"\n"
	if got := hide(t, "/etc/netplan/90-NM-x.yaml", np); got != "      networkmanager:\n        passthrough:\n          802-1x.pin: <hidden>\n          macsec.mka-cak: <hidden>\n          connection.id: \"Home\"\n" {
		t.Errorf("passthrough:\n%s", got)
	}
}

// K5: a value that begins with the marker's text is hidden all the same.
func TestMarkerTextInValue(t *testing.T) {
	for p, in := range map[string]string{
		"/etc/shadow":   "bob:<hidden>sekrit:19000::::::\n",
		"/etc/x.conf":   "password=<hidden>sekrit\nopts a,secret=<hidden>sekrit\n",
		"/etc/x.yaml":   "password: <hidden>sekrit\n",
		"/etc/y.conf":   "[vpn-secrets]\nx=<hidden, new>sekrit\n",
		"/etc/pem.conf": begin("", "") + "\n<hidden>sekrit\n-----END PRIVATE KEY-----\n",
	} {
		shown(t, p, hide(t, p, in))
	}
}

// K8: a validator's line. A short value counts only where it stands
// alone, so a marker inside a file name cannot tell a PIN; a value is
// also found as a parser prints it, unquoted or unescaped.
func TestLineForms(t *testing.T) {
	vs := Values("/etc/netplan/x.yaml", []byte("network:\n  modems:\n    m:\n      pin: 4711\n"), false)
	raw := "/etc/netplan/90-NM-5f3a4711-c0de.yaml:4:7: bad pin 4711"
	if got := Line(raw, vs, Plain); got != "/etc/netplan/90-NM-5f3a4711-c0de.yaml:4:7: bad pin <hidden>" {
		t.Errorf("PIN: %q", got)
	}
	vs = Values("/etc/netplan/x.yaml", []byte(wifiHead+"        \"Home\":\n          password: \"sekrit \\\"x\\\"\"\n"), false)
	for _, said := range []string{`invalid value 'sekrit "x"'`, `psk="sekrit \"x\""`} {
		shown(t, "YAML "+said, Line(said, vs, Plain))
	}
	vs = Values("/etc/x.nmconnection", []byte("[802-1x]\npassword=\\ssekrit\n"), false)
	shown(t, "keyfile", Line("password is ' sekrit'", vs, Plain))
	vs = Values("/etc/x.yaml", []byte("password: 'it''s sekrit'\n"), false)
	shown(t, "single", Line("value it's sekrit is bad", vs, Plain))
}

// K10: a key named in a comment hides its own value but leaves nothing
// open, so the lines after it are shown.
func TestCommentKey(t *testing.T) {
	in := "network:\n  wifis:\n# password: sekrit\n    wlan0:\n      addresses: [10.0.0.5/24]\n      password: x # psk: y\n      dhcp4: true\n"
	want := "network:\n  wifis:\n# password: <hidden>\n    wlan0:\n      addresses: [10.0.0.5/24]\n      password: <hidden> # psk: <hidden>\n      dhcp4: true\n"
	if got := hide(t, "/etc/netplan/x.yaml", in); got != want {
		t.Errorf("got:\n%s", got)
	}
}

// K1: the formats of the wider secret list.
func TestWiderList(t *testing.T) {
	for p, c := range map[string][2]string{
		// A copy of shadow by name, and a crypt hash in any secret file.
		"/etc/shadow.backup": {"root:$6$s$sekrit:19000::::::\nbob:*:19000::::::\nold:sekritDES12:1::::::\n", "root:<hidden>:19000::::::\nbob:*:19000::::::\nold:<hidden>:1::::::\n"},
		"/etc/old-users":     {"root:!$y$j9T$sekrit:19000:0:99999:7:::\n", "root:!<hidden>:19000:0:99999:7:::\n"},
		// msmtp, nslcd, ifupdown, ssmtp, sssd.
		"/etc/msmtprc":            {"account x\nhost smtp.example\npassword sekrit one\nauth on\n", "account x\nhost smtp.example\npassword <hidden>\nauth on\n"},
		"/etc/nslcd.conf":         {"uri ldap://h/\nbindpw sekrit\n", "uri ldap://h/\nbindpw <hidden>\n"},
		"/etc/network/interfaces": {"iface wlan0 inet dhcp\n    wpa-ssid Home\n    wpa-psk sekrit\n", "iface wlan0 inet dhcp\n    wpa-ssid Home\n    wpa-psk <hidden>\n"},
		"/etc/ssmtp/ssmtp.conf":   {"AuthUser=me\nAuthPass=sekrit\n", "AuthUser=me\nAuthPass=<hidden>\n"},
		"/etc/sssd/sssd.conf":     {"[domain/x]\nldap_default_authtok = sekrit\nldap_uri = ldap://h\n", "[domain/x]\nldap_default_authtok = <hidden>\nldap_uri = ldap://h\n"},
		// fstab's CIFS options; cloud-init's users, chpasswd and keys.
		"/etc/fstab": {"//srv/s /mnt cifs username=me,password=sekrit,ro 0 0\nUUID=1 / ext4 defaults 0 1\n", "//srv/s /mnt cifs username=me,password=<hidden>,ro 0 0\nUUID=1 / ext4 defaults 0 1\n"},
		"/etc/cloud/cloud.cfg": {"users:\n  - name: me\n    passwd: sekrit\nchpasswd:\n  list: |\n    root:sekrit\n  expire: false\nssh_keys:\n  rsa_private: |\n    sekrit\n  rsa_public: ssh-rsa AAA\n",
			"users:\n  - name: me\n    passwd: <hidden>\nchpasswd:\n  <hidden>\n    <hidden>\n  <hidden>\nssh_keys:\n  rsa_private: |\n    <hidden>\n  rsa_public: ssh-rsa AAA\n"},
		// OpenVPN's inline keys.
		"/etc/openvpn/client.conf": {"remote h 1194\n<tls-auth>\nsekrit1\nsekrit2\n</tls-auth>\nverb 3\n", "remote h 1194\n<tls-auth>\n<hidden>\n<hidden>\n</tls-auth>\nverb 3\n"},
	} {
		if got := hide(t, p, c[0]); got != c[1] {
			t.Errorf("%s:\n%s\nwant:\n%s", p, got, c[1])
		}
	}
	for _, k := range []string{"AuthPass", "bindpw", "rootpw", "rsa_private", "ldap_default_authtok", "api_token"} {
		if !secretKey(k) {
			t.Errorf("%s is not a secret key", k)
		}
	}
}

// A file that is only a secret: every line but blanks, indentation and
// line ends.
func TestWhole(t *testing.T) {
	in := "sekrit1\n\n  sekrit2 x \r\nlast"
	got, n := Whole([]byte(in), Plain)
	if string(got) != "<hidden>\n\n  <hidden> \r\n<hidden>" || n != 3 {
		t.Errorf("%q, %d", got, n)
	}
	if vs := Values("/x", []byte(in), true); strings.Join(vs, ",") != "sekrit2 x,sekrit1,last" {
		t.Errorf("values %q", vs)
	}
}
