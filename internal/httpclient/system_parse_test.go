package httpclient

import "testing"

func TestDarwinSettings(t *testing.T) {
	s, err := parseDarwinSettings(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : 10.0.0.0/8
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPProxy : 127.0.0.1
  HTTPPort : 10809
  HTTPSEnable : 1
  HTTPSProxy : ::1
  HTTPSPort : 10809
  SOCKSEnable : 1
  SOCKSProxy : localhost
  SOCKSPort : 10808
  ProxyAutoConfigEnable : 1
  __SCOPED__ : <dictionary> {
    en0 : <dictionary> {
      HTTPEnable : 1
      HTTPProxy : ignored.test
      HTTPPort : 9000
    }
  }
}`)
	if err != nil {
		t.Fatal(err)
	}
	if s.HTTP.String() != "http://127.0.0.1:10809" || s.HTTPS.String() != "http://[::1]:10809" || s.SOCKS.String() != "socks5://localhost:10808" || len(s.Bypass) != 3 || !s.ExcludeSimple || !s.Auto {
		t.Fatalf("%+v", s)
	}
	for _, raw := range []string{"", "garbage", "<dictionary> {", "<dictionary> {\n HTTPEnable : 1\n}", "<dictionary> {\n HTTPEnable : 1\n HTTPProxy : proxy.test\n HTTPPort : 70000\n}"} {
		if _, err := parseDarwinSettings(raw); err == nil {
			t.Fatalf("accepted invalid snapshot %q", raw)
		}
	}
	s, err = parseDarwinSettings("<dictionary> {\n}")
	if err != nil || s.HTTP != nil || s.Auto {
		t.Fatalf("empty snapshot %+v %v", s, err)
	}
}

func TestWindowsSettings(t *testing.T) {
	s, err := parseWindowsSettings("http=127.0.0.1:10809;https=secure.test:8080;socks=[::1]:10808;ftp=unused:21", "<local>;*.internal;10.*", true)
	if err != nil {
		t.Fatal(err)
	}
	if s.HTTP.String() != "http://127.0.0.1:10809" || s.HTTPS.String() != "http://secure.test:8080" || s.SOCKS.String() != "socks5://[::1]:10808" || len(s.Bypass) != 3 || !s.Auto {
		t.Fatalf("%+v", s)
	}
	s, err = parseWindowsSettings("proxy.test:8080", "", false)
	if err != nil || s.HTTP.String() != s.HTTPS.String() {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err = parseWindowsSettings("http=http://user:secret@%zz", "", false); err == nil {
		t.Fatal("accepted invalid proxy")
	}
}
