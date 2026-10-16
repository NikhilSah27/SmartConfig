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

// Numbered gives a value the same number wherever it is, so the diff of
// two versions shows which secret changed and which did not.
func TestNumbered(t *testing.T) {
	h := Numbered()
	a, _ := File("/etc/shadow", []byte("a:$6$one:1\nb:$6$two:1\n"), h)
	b, _ := File("/etc/shadow", []byte("a:$6$one:1\nb:$6$three:1\nc:$6$one:1\n"), h)
	if string(a) != "a:<hidden 1>:1\nb:<hidden 2>:1\n" || string(b) != "a:<hidden 1>:1\nb:<hidden 3>:1\nc:<hidden 1>:1\n" {
		t.Errorf("a %q, b %q", a, b)
	}
}

func TestValuesAndLine(t *testing.T) {
	data := []byte("[wifi-security]\npsk=hunter22\n[802-1x]\npin=12\npassword=ab\n")
	vs := Values("/etc/NetworkManager/system-connections/x", data)
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
