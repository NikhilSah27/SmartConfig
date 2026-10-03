package scope

import (
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern string
		yes, no []string
	}{
		// ** matches zero and many segments; /a/** matches /a itself.
		{`/a/**`, []string{"/a", "/a/b", "/a/b/c/d"}, []string{"/", "/ab", "/b/a"}},
		{`/a/**/z`, []string{"/a/z", "/a/b/z", "/a/b/c/z"}, []string{"/a", "/a/z/b", "/az"}},
		{`**/*.dpkg-old`, []string{"/x.dpkg-old", "/etc/a/b.dpkg-old"}, []string{"/etc/a.dpkg-old/b"}},
		{`/**`, []string{"/", "/etc", "/etc/a/b"}, nil},
		{`/a/**/**/z`, []string{"/a/z", "/a/b/c/z"}, []string{"/a/b"}},
		{`**/*`, []string{"/etc"}, []string{"/"}},
		// * stays within one segment and matches a leading dot.
		{`/etc/*`, []string{"/etc/hosts", "/etc/.pwd.lock", "/etc/a b"}, []string{"/etc", "/etc/a/b"}},
		{`**/*~`, []string{"/etc/.foo~", "/etc/fstab~"}, []string{"/etc/fstab"}},
		{`/etc/.*.sw?`, []string{"/etc/.fstab.swp", "/etc/.a.swx"}, []string{"/etc/fstab.swp", "/etc/.a.sw"}},
		// Classes: [^x] negates, [!x] is literal, - escaped.
		{`/etc/[^a]*`, []string{"/etc/bcd", "/etc/.x"}, []string{"/etc/abc"}},
		{`/etc/[!x]*`, []string{"/etc/!abc", "/etc/xyz"}, []string{"/etc/abc"}},
		{`/etc/snap[.\-]*`, []string{"/etc/snap.a", "/etc/snap-a"}, []string{"/etc/snapa", "/etc/snap"}},
		// Escapes.
		{`/etc/a\*`, []string{"/etc/a*"}, []string{"/etc/ab", "/etc/a"}},
		{`/etc/a\{b,c\}`, []string{"/etc/a{b,c}"}, []string{"/etc/ab"}},
		{`/etc/a\\b`, []string{`/etc/a\b`}, []string{"/etc/ab"}},
		// ** only counts bare (chunk C review D6).
		{`/a/\**`, []string{"/a/*", "/a/*x"}, []string{"/a/x", "/a/b/c"}},
		{`/a/[**]`, []string{"/a/*"}, []string{"/a/x", "/a/**"}},
		{`/a/*\*`, []string{"/a/x*", "/a/*"}, []string{"/a/x"}},
		{`/a/*b*`, []string{"/a/b", "/a/xbx"}, []string{"/a/x"}},
		// Braces: several groups, an empty alternative, * and / inside.
		{`/home/*/.ssh/authorized_keys{,2}`,
			[]string{"/home/u/.ssh/authorized_keys", "/home/u/.ssh/authorized_keys2"},
			[]string{"/home/u/.ssh/authorized_keys3", "/home/u/.ssh/id_rsa"}},
		{`/{a,b}/{x,y}`, []string{"/a/x", "/a/y", "/b/x", "/b/y"}, []string{"/a/b", "/x/a"}},
		{`/etc/{*.bak,sub/*}`, []string{"/etc/f.bak", "/etc/sub/f"}, []string{"/etc/sub/f.x/g", "/etc/f"}},
		{`/etc/{x,**}/z`, []string{"/etc/x/z", "/etc/z", "/etc/a/b/z"}, []string{"/etc/x"}},
		{`/etc/a[,]b{c,d}`, []string{"/etc/a,bc", "/etc/a,bd"}, []string{"/etc/abc"}},
		{`/etc/{a[,]b,c}`, []string{"/etc/a,b", "/etc/c"}, []string{"/etc/a", "/etc/b"}},
		{`/etc/x[{]`, []string{"/etc/x{"}, []string{"/etc/x"}},
		// Raw names: a backslash, UTF-8 (? is one character), invalid UTF-8.
		{`/etc/*`, []string{`/etc/a\b`}, nil},
		{`/etc/caf?`, []string{"/etc/café", "/etc/cafe"}, []string{"/etc/caf", "/etc/caféé"}},
		{`/etc/?`, []string{"/etc/\xff"}, []string{"/etc/\xff\xfe"}},
		{`/etc/*.conf`, []string{"/etc/\xff.conf", "/etc/ü.conf"}, nil},
		// Not absolute or not clean input never matches a pattern it should not.
		{`/etc/hosts`, []string{"/etc/hosts"}, []string{"etc/hosts", "", "/etc/hosts/"}},
		{`**/hosts`, []string{"/etc/hosts", "/hosts"}, []string{"hosts", "etc/hosts", "/etc/hosts.d"}},
	} {
		g, err := compileGlob(c.pattern)
		if err != nil {
			t.Errorf("compile %q: %v", c.pattern, err)
			continue
		}
		for _, p := range c.yes {
			if !g.match(p) {
				t.Errorf("%q does not match %q", c.pattern, p)
			}
		}
		for _, p := range c.no {
			if g.match(p) {
				t.Errorf("%q matches %q", c.pattern, p)
			}
		}
	}
}

func TestGlobErrors(t *testing.T) {
	for _, c := range []struct{ pattern, err string }{
		{`etc/hosts`, "must start with / or **/"},
		{`*/hosts`, "must start with / or **/"},
		{``, "must start with / or **/"},
		{`/etc/{a,{b,c}}`, "nested braces"},
		{`/etc/{a,b`, "{ without }"},
		{`/etc/a,b}`, "} without {"},
		{`/etc/snap[-.]*`, "syntax error in pattern"}, // fails even when no name matches
		{`/etc/[abc`, "syntax error in pattern"},
		{`/etc/a\`, "syntax error in pattern"},
		{`/etc/a**`, "** must be a whole segment"},
		{`/etc/**b/c`, "** must be a whole segment"},
		{`/etc/a\\**`, "** must be a whole segment"},
		{`/etc/[a]**`, "** must be a whole segment"},
		{`/etc//hosts`, "empty segment"},
		{`/etc/`, "empty segment"},
		{`/`, "empty segment"},
		{`/etc/../hosts`, `segment ".."`},
		{`/etc/./hosts`, `segment "."`},
		{`/etc/{a,}/`, "empty segment"},
	} {
		_, err := compileGlob(c.pattern)
		if err == nil {
			t.Errorf("%q: no error, want %q", c.pattern, c.err)
			continue
		}
		if !strings.Contains(err.Error(), c.err) || !strings.Contains(err.Error(), "pattern ") {
			t.Errorf("%q: %v, want %q", c.pattern, err, c.err)
		}
	}
}

func TestGlobExpandOrder(t *testing.T) {
	got, err := expand(`/{a,b}{1,,2}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/a1 /a /a2 /b1 /b /b2"; strings.Join(got, " ") != want {
		t.Fatalf("%q, want %q", got, want)
	}
	g, _ := compileGlob(`/x/{a,b}`)
	if g.String() != `/x/{a,b}` {
		t.Fatalf("String() = %q", g)
	}
}

// The exported Glob is the same language as the scope's lines.
func TestExportedGlob(t *testing.T) {
	g, err := CompileGlob("/etc/systemd/system/*.{service,timer}")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Match("/etc/systemd/system/a.timer") || g.Match("/etc/systemd/system/d/a.timer") || g.Match("/etc/systemd/system/a.mount") {
		t.Error("match")
	}
	if g.String() != "/etc/systemd/system/*.{service,timer}" {
		t.Errorf("String() = %q", g.String())
	}
	if _, err := CompileGlob("etc/x"); err == nil {
		t.Error("relative pattern accepted")
	}
}
