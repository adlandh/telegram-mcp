package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type codeReply struct {
	code tg.AuthSentCodeClass
	err  error
}

type loginClient struct {
	status               auth.Status
	statusErr            error
	replies              []codeReply
	signErr, passwordErr []error // consumed per call; nil once exhausted
	calls                []string
}

func (c *loginClient) Status(context.Context) (*auth.Status, error) {
	c.calls = append(c.calls, "status")
	return &c.status, c.statusErr
}

func (c *loginClient) SendCode(_ context.Context, phone string, _ auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	c.calls = append(c.calls, "send:"+phone)
	return c.reply()
}

func (c *loginClient) ResendCode(_ context.Context, phone, hash string) (tg.AuthSentCodeClass, error) {
	c.calls = append(c.calls, "resend:"+phone+":"+hash)
	return c.reply()
}

func (c *loginClient) reply() (tg.AuthSentCodeClass, error) {
	if len(c.replies) == 0 {
		return nil, errors.New("unexpected code request")
	}
	r := c.replies[0]
	c.replies = c.replies[1:]
	return r.code, r.err
}

func (c *loginClient) SignIn(_ context.Context, phone, code, hash string) (*tg.AuthAuthorization, error) {
	c.calls = append(c.calls, "signin:"+phone+":"+code+":"+hash)
	return &tg.AuthAuthorization{}, nextErr(&c.signErr)
}

func (c *loginClient) Password(_ context.Context, password string) (*tg.AuthAuthorization, error) {
	c.calls = append(c.calls, "password:"+password)
	return &tg.AuthAuthorization{}, nextErr(&c.passwordErr)
}

func nextErr(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}

func sentCode(hash string) *tg.AuthSentCode {
	return &tg.AuthSentCode{PhoneCodeHash: hash, Type: &tg.AuthSentCodeTypeApp{}}
}

func TestSetupLogin(t *testing.T) {
	// Capture process stdout too: passing a diagnostic writer must not hide stray prints.
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = stdout
	t.Cleanup(func() { os.Stdout = previous; _ = stdout.Close() })
	for _, tt := range []struct {
		name    string
		prepare func(*loginClient)
		inputs  []string
		want    string
		calls   []string
	}{
		{"direct", nil, []string{"secret-code"}, "", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash"}},
		{"authorized", func(c *loginClient) { c.status.Authorized = true }, nil, "", []string{"status"}},
		{"status error", func(c *loginClient) { c.statusErr = errors.New("secret-payload") }, nil, "check authorization", []string{"status"}},
		{"success response", func(c *loginClient) {
			c.replies[0].code = &tg.AuthSentCodeSuccess{Authorization: &tg.AuthAuthorization{}}
		}, nil, "", []string{"status", "send:secret-phone"}},
		{"signup response", func(c *loginClient) {
			c.replies[0].code = &tg.AuthSentCodeSuccess{Authorization: &tg.AuthAuthorizationSignUpRequired{}}
		}, nil, "registration is not supported", []string{"status", "send:secret-phone"}},
		{"signup signin", func(c *loginClient) { c.signErr = []error{&auth.SignUpRequired{}} }, []string{"secret-code"}, "registration is not supported", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash"}},
		{"2FA", func(c *loginClient) { c.signErr = []error{auth.ErrPasswordAuthNeeded} }, []string{"secret-code", " secret-password "}, "", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash", "password: secret-password "}},
		{"initial 2FA", func(c *loginClient) { c.replies[0].err = tgerr.New(401, "SESSION_PASSWORD_NEEDED") }, []string{" secret-password "}, "", []string{"status", "send:secret-phone", "password: secret-password "}},
		{"wrong password", func(c *loginClient) {
			c.signErr = []error{auth.ErrPasswordAuthNeeded}
			c.passwordErr = []error{auth.ErrPasswordInvalid}
		}, []string{"secret-code", "secret-wrong", "secret-password"}, "", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash", "password:secret-wrong", "password:secret-password"}},
		{"nil response", func(c *loginClient) { c.replies[0].code = nil }, nil, "unsupported Telegram authorization", []string{"status", "send:secret-phone"}},
		{"typed nil response", func(c *loginClient) { c.replies[0].code = (*tg.AuthSentCode)(nil) }, nil, "incomplete", []string{"status", "send:secret-phone"}},
		{"nil delivery", func(c *loginClient) { c.replies[0].code = &tg.AuthSentCode{PhoneCodeHash: "secret-hash"} }, nil, "unsupported Telegram code delivery", []string{"status", "send:secret-phone"}},
		{"unsupported delivery", func(c *loginClient) {
			c.replies[0].code = &tg.AuthSentCode{PhoneCodeHash: "secret-hash", Type: &tg.AuthSentCodeTypeEmailCode{EmailPattern: "secret-email"}}
		}, nil, "official Telegram client", []string{"status", "send:secret-phone"}},
		{"flood wait", func(c *loginClient) { c.replies[0].err = tgerr.New(420, "FLOOD_WAIT_30") }, nil, "retry after 30s", []string{"status", "send:secret-phone"}},
		{"invalid code", func(c *loginClient) { c.signErr = []error{tgerr.New(400, "PHONE_CODE_INVALID")} }, []string{"secret-typo", "secret-code"}, "", []string{"status", "send:secret-phone", "signin:secret-phone:secret-typo:secret-hash", "signin:secret-phone:secret-code:secret-hash"}},
		{"expired code", func(c *loginClient) { c.signErr = []error{tgerr.New(400, "PHONE_CODE_EXPIRED")} }, []string{"secret-code"}, "PHONE_CODE_EXPIRED", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash"}},
		{"sensitive error", func(c *loginClient) {
			c.replies[0].err = fmt.Errorf("secret-payload: %w", tgerr.New(400, "secret-rpc"))
		}, nil, "request login code: failed", []string{"status", "send:secret-phone"}},
		{"unavailable resend", nil, []string{"resend", "", "secret-code"}, "", []string{"status", "send:secret-phone", "signin:secret-phone:secret-code:secret-hash"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &loginClient{replies: []codeReply{{code: sentCode("secret-hash")}}}
			if tt.prepare != nil {
				tt.prepare(client)
			}
			var out bytes.Buffer
			inputs := slices.Clone(tt.inputs)
			read := func(_ context.Context, label string, trim bool) (string, error) {
				if len(inputs) == 0 {
					t.Fatalf("unexpected prompt %q", label)
				}
				if trim == strings.Contains(label, "password") {
					t.Fatal("incorrect credential trimming")
				}
				input := inputs[0]
				inputs = inputs[1:]
				return input, nil
			}
			err := setupLogin(t.Context(), client, "secret-phone", read, &out, time.Now)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if !slices.Equal(client.calls, tt.calls) || len(inputs) != 0 {
				t.Fatalf("calls = %v, want %v; remaining input = %d", client.calls, tt.calls, len(inputs))
			}
			if strings.Contains(fmt.Sprint(err)+out.String(), "secret-") {
				t.Fatal("credentials or response payload leaked")
			}
		})
	}
	info, err := stdout.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatalf("setup wrote to stdout: %v", err)
	}
}

func TestSetupResend(t *testing.T) {
	for _, ending := range []string{"signin", "error"} {
		t.Run(ending, func(t *testing.T) {
			first, second := sentCode("hash1"), sentCode("hash2")
			first.SetNextType(&tg.AuthCodeTypeSMS{})
			first.SetTimeout(30)
			second.SetNextType(&tg.AuthCodeTypeSMS{})
			second.SetTimeout(10)
			third := codeReply{code: sentCode("hash3")}
			if ending == "error" {
				third.err = tgerr.New(420, "FLOOD_WAIT_60")
			}
			client := &loginClient{replies: []codeReply{{code: first}, {code: second}, third}}
			current := time.Unix(0, 0)
			reads := 0
			read := func(context.Context, string, bool) (string, error) {
				reads++
				switch reads {
				case 1: // Too early for hash1.
				case 2:
					current = current.Add(30 * time.Second)
				case 3: // New timeout starts after the first resend.
				case 4:
					current = current.Add(10 * time.Second)
				case 5:
					if ending != "signin" {
						t.Fatal("prompt after terminal response")
					}
					return "code3", nil
				default:
					t.Fatal("unexpected prompt")
				}
				return "resend", nil
			}
			var out bytes.Buffer
			err := setupLogin(t.Context(), client, "phone", read, &out, func() time.Time { return current })
			want := []string{"status", "send:phone", "resend:phone:hash1", "resend:phone:hash2"}
			if ending == "signin" {
				want = append(want, "signin:phone:code3:hash3")
			}
			if !slices.Equal(client.calls, want) {
				t.Fatalf("calls = %v, want %v", client.calls, want)
			}
			wantError := ending == "error"
			if (err != nil) != wantError {
				t.Fatalf("error = %v", err)
			}
			if ending == "error" && !strings.Contains(err.Error(), "retry after 1m0s") {
				t.Fatalf("missing rate limit: %v", err)
			}
			if !strings.Contains(out.String(), "Wait 30s") || !strings.Contains(out.String(), "Wait 10s") {
				t.Fatalf("missing timeout guidance: %s", &out)
			}
		})
	}
}

func TestSetupMissingTimeout(t *testing.T) {
	first := sentCode("hash1")
	first.SetNextType(&tg.AuthCodeTypeSMS{})
	client := &loginClient{replies: []codeReply{{code: first}, {code: &tg.AuthSentCodeSuccess{Authorization: &tg.AuthAuthorization{}}}}}
	read := func(context.Context, string, bool) (string, error) { return "resend", nil }
	var out bytes.Buffer
	if err := setupLogin(t.Context(), client, "phone", read, &out, time.Now); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.calls, []string{"status", "send:phone", "resend:phone:hash1"}) {
		t.Fatalf("unexpected calls: %v", client.calls)
	}
}

func TestSetupPromptFailureAndCancellation(t *testing.T) {
	for _, phase := range []string{"before status", "code error", "code cancel", "password error", "password cancel", "terminal"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &loginClient{replies: []codeReply{{code: sentCode("hash")}}}
			failure := errors.New("secret prompt failure")
			if strings.HasPrefix(phase, "password") {
				client.replies[0].err = auth.ErrPasswordAuthNeeded
			}
			if phase == "before status" {
				cancel()
			}
			read := func(context.Context, string, bool) (string, error) {
				if strings.HasSuffix(phase, "cancel") {
					cancel()
					return "secret", nil
				}
				if phase == "terminal" {
					return "", errInteractiveTerminal
				}
				return "", failure
			}
			var out bytes.Buffer
			err := setupLogin(ctx, client, "phone", read, &out, time.Now)
			want := failure
			if phase == "before status" || strings.HasSuffix(phase, "cancel") {
				want = context.Canceled
			} else if phase == "terminal" {
				want = errInteractiveTerminal
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("got %v, want safe wrapped %v", err, want)
			}
			if phase == "terminal" && !strings.Contains(err.Error(), "interactive terminal") {
				t.Fatal("missing terminal guidance")
			}
			wantCalls := []string{"status", "send:phone"}
			if phase == "before status" {
				wantCalls = nil
			}
			if !slices.Equal(client.calls, wantCalls) {
				t.Fatalf("calls after failure: %v", client.calls)
			}
		})
	}
}

type qrLoginClient struct {
	authErr     error
	passwordErr []error
	shown       []string
	calls       []string
}

func (c *qrLoginClient) Auth(ctx context.Context, _ qrlogin.LoggedIn, show func(context.Context, qrlogin.Token) error, _ ...int64) (*tg.AuthAuthorization, error) {
	if show == nil {
		return nil, errors.New("missing show callback")
	}
	token := qrlogin.NewToken([]byte("token"), 1)
	if err := show(ctx, token); err != nil {
		return nil, err
	}
	c.shown = append(c.shown, token.URL())
	return &tg.AuthAuthorization{}, c.authErr
}

func (c *qrLoginClient) Password(_ context.Context, password string) (*tg.AuthAuthorization, error) {
	c.calls = append(c.calls, "password:"+password)
	return &tg.AuthAuthorization{}, nextErr(&c.passwordErr)
}

func TestSetupQR(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = stdout
	t.Cleanup(func() { os.Stdout = previous; _ = stdout.Close() })
	client := &qrLoginClient{}
	var out bytes.Buffer
	unexpectedPrompt := func(context.Context, string, bool) (string, error) { t.Fatal("unexpected prompt"); return "", nil }
	if err := setupQR(t.Context(), client, client, nil, &out, unexpectedPrompt); err != nil {
		t.Fatal(err)
	}
	if len(client.shown) != 1 || !strings.HasPrefix(client.shown[0], "tg://login?token=") {
		t.Fatalf("unexpected shown tokens: %v", client.shown)
	}
	if !strings.HasPrefix(out.String(), "Scan the QR code") || len(out.String()) < 200 {
		t.Fatalf("missing QR rendering: %q", &out)
	}
	info, err := stdout.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatalf("setup qr wrote to stdout: %v", err)
	}
}

func TestSetupQRPassword(t *testing.T) {
	for _, authErr := range []error{auth.ErrPasswordAuthNeeded, tgerr.New(401, "SESSION_PASSWORD_NEEDED")} {
		client := &qrLoginClient{authErr: authErr}
		var out bytes.Buffer
		read := func(_ context.Context, label string, trim bool) (string, error) {
			if trim || !strings.Contains(label, "password") {
				t.Fatal("incorrect 2FA prompt")
			}
			return " secret-password ", nil
		}
		if err := setupQR(t.Context(), client, client, nil, &out, read); err != nil {
			t.Fatalf("error = %v for %v", err, authErr)
		}
		if !slices.Equal(client.calls, []string{"password: secret-password "}) {
			t.Fatalf("calls = %v, want untrimmed 2FA password", client.calls)
		}
		if strings.Contains(out.String(), "secret-") {
			t.Fatal("password leaked to diagnostics")
		}
	}
	t.Run("retry incorrect password", func(t *testing.T) {
		client := &qrLoginClient{authErr: auth.ErrPasswordAuthNeeded, passwordErr: []error{auth.ErrPasswordInvalid}}
		var out bytes.Buffer
		inputs := []string{"secret-wrong", "secret-password"}
		read := func(context.Context, string, bool) (string, error) {
			input := inputs[0]
			inputs = inputs[1:]
			return input, nil
		}
		if err := setupQR(t.Context(), client, client, nil, &out, read); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(client.calls, []string{"password:secret-wrong", "password:secret-password"}) || !strings.Contains(out.String(), "Incorrect 2FA password") || strings.Contains(out.String(), "secret-") {
			t.Fatalf("calls = %v, out = %q", client.calls, &out)
		}
	})
}

func TestSetupQRFailureAndCancellation(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		client := &qrLoginClient{authErr: tgerr.New(420, "FLOOD_WAIT_30")}
		var out bytes.Buffer
		err := setupQR(t.Context(), client, client, nil, &out, nil)
		if err == nil || !strings.Contains(err.Error(), "retry after 30s") || strings.Contains(err.Error(), "token") {
			t.Fatalf("got %v, want safe wrapped rate limit", err)
		}
	})
	t.Run("rejection names the rpc type", func(t *testing.T) {
		client := &qrLoginClient{authErr: tgerr.New(400, "AUTH_TOKEN_EXPIRED")}
		var out bytes.Buffer
		err := setupQR(t.Context(), client, client, nil, &out, nil)
		if err == nil || !strings.Contains(err.Error(), "AUTH_TOKEN_EXPIRED") {
			t.Fatalf("got %v, want explicit rejection", err)
		}
	})
	t.Run("plain failure hides details", func(t *testing.T) {
		client := &qrLoginClient{authErr: errors.New("secret-boom")}
		var out bytes.Buffer
		err := setupQR(t.Context(), client, client, nil, &out, nil)
		if err == nil || strings.Contains(err.Error(), "secret-boom") {
			t.Fatalf("got %v, want safe generic failure", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := setupQR(ctx, &qrLoginClient{}, &qrLoginClient{}, nil, &bytes.Buffer{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	})
}

func TestSetupDeliveryChannels(t *testing.T) {
	for _, tt := range []struct {
		name string
		kind tg.AuthSentCodeTypeClass
		code string
		want string
	}{
		{"app", &tg.AuthSentCodeTypeApp{}, "12345", "in-app service notification"},
		{"sms", &tg.AuthSentCodeTypeSMS{}, "12345", "SMS (check"},
		{"word", &tg.AuthSentCodeTypeSMSWord{}, "hello", "SMS containing a word"},
		{"phrase", &tg.AuthSentCodeTypeSMSPhrase{}, "hello  world", "SMS containing a phrase"},
		{"call", &tg.AuthSentCodeTypeCall{}, "12345", "voice call"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &loginClient{replies: []codeReply{{code: &tg.AuthSentCode{PhoneCodeHash: "hash", Type: tt.kind}}}}
			var out bytes.Buffer
			read := func(context.Context, string, bool) (string, error) {
				if !strings.Contains(out.String(), tt.want) || !strings.Contains(out.String(), "Receipt is not confirmed") {
					t.Fatalf("missing delivery information before input: %s", &out)
				}
				return tt.code, nil
			}
			if err := setupLogin(t.Context(), client, "phone", read, &out, time.Now); err != nil {
				t.Fatal(err)
			}
			if got := client.calls[len(client.calls)-1]; got != "signin:phone:"+tt.code+":hash" {
				t.Fatalf("input changed: %q", got)
			}
		})
	}
}

func TestSetupRetryKeepsResendDeadline(t *testing.T) {
	first := sentCode("hash1")
	first.SetNextType(&tg.AuthCodeTypeSMS{})
	first.SetTimeout(30)
	client := &loginClient{replies: []codeReply{{code: first}, {code: sentCode("hash2")}}, signErr: []error{tgerr.New(400, "PHONE_CODE_INVALID")}}
	current := time.Unix(0, 0)
	inputs := []string{"typo", "resend", "code2"}
	read := func(context.Context, string, bool) (string, error) {
		if len(inputs) == 0 {
			t.Fatal("unexpected prompt")
		}
		input := inputs[0]
		inputs = inputs[1:]
		if input == "typo" {
			current = current.Add(30 * time.Second) // original timeout elapses before the typo
		}
		return input, nil
	}
	var out bytes.Buffer
	if err := setupLogin(t.Context(), client, "phone", read, &out, func() time.Time { return current }); err != nil {
		t.Fatal(err)
	}
	want := []string{"status", "send:phone", "signin:phone:typo:hash1", "resend:phone:hash1", "signin:phone:code2:hash2"}
	if !slices.Equal(client.calls, want) || strings.Contains(out.String(), "Wait ") {
		t.Fatalf("calls = %v, out = %s", client.calls, &out)
	}
}
