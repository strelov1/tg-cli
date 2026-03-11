package main

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
)

// ── Pending auth state ──

// pendingAuth holds the phone code hash between auth-request and auth-complete.
// Stored at ~/.tg-cli/sessions/<phone>/pending_auth.json.
type pendingAuth struct {
	Phone         string    `json:"phone"`
	PhoneCodeHash string    `json:"phone_code_hash"`
	CreatedAt     time.Time `json:"created_at"`
}

const pendingAuthTTL = 5 * time.Minute

func (c config) pendingAuthFile() string {
	return filepath.Join(c.accountDir(), "pending_auth.json")
}

func savePendingAuth(c config, hash string) error {
	if err := os.MkdirAll(c.accountDir(), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(pendingAuth{
		Phone:         c.account,
		PhoneCodeHash: hash,
		CreatedAt:     time.Now().UTC(),
	})
	return os.WriteFile(c.pendingAuthFile(), data, 0600)
}

func loadPendingAuth(c config) (pendingAuth, error) {
	data, err := os.ReadFile(c.pendingAuthFile())
	if err != nil {
		return pendingAuth{}, fmt.Errorf("no pending auth for %s — run: tg-cli auth-request %s", c.account, c.account)
	}
	var pa pendingAuth
	if err := json.Unmarshal(data, &pa); err != nil {
		return pendingAuth{}, fmt.Errorf("corrupt pending auth file: %w", err)
	}
	if !pa.CreatedAt.IsZero() && time.Since(pa.CreatedAt) > pendingAuthTTL {
		os.Remove(c.pendingAuthFile())
		return pendingAuth{}, fmt.Errorf("auth code expired (codes are valid for 5 minutes) — re-run: tg-cli auth-request %s", c.account)
	}
	return pa, nil
}

// ── Auth commands ──

// cmdAuthRequest sends the verification code to the phone (step 1).
// Non-blocking — exits immediately after the code is sent.
func cmdAuthRequest(c config, phone string) error {
	c.account = phone
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		appID, err := c.appIDInt()
		if err != nil {
			return err
		}
		authClient := auth.NewClient(api, cryptorand.Reader, appID, c.apiHash)
		sent, err := authClient.SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			return fmt.Errorf("send code: %w", err)
		}
		sentCode, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("unexpected response type: %T", sent)
		}
		if err := savePendingAuth(c, sentCode.PhoneCodeHash); err != nil {
			return fmt.Errorf("save pending auth: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Code sent to %s\nRun: tg-cli auth-complete %s --code <code> --password <2fa>\n", phone, phone)
		return printJSON(map[string]any{
			"status": "code_sent",
			"phone":  phone,
		})
	})
}

// cmdAuthComplete finishes authorization with the received code (step 2).
func cmdAuthComplete(c config, phone, code, password string) error {
	c.account = phone
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		appID, err := c.appIDInt()
		if err != nil {
			return err
		}
		pa, err := loadPendingAuth(c)
		if err != nil {
			return err
		}
		authClient := auth.NewClient(api, cryptorand.Reader, appID, c.apiHash)
		_, err = authClient.SignIn(ctx, phone, code, pa.PhoneCodeHash)
		if err == auth.ErrPasswordAuthNeeded {
			if password == "" {
				return fmt.Errorf("2FA required — re-run with: --password <2fa_password>")
			}
			if _, err := authClient.Password(ctx, password); err != nil {
				return fmt.Errorf("2FA check failed: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("sign in: %w", err)
		}

		os.Remove(c.pendingAuthFile())

		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(self.FirstName + " " + self.LastName)
		fmt.Fprintf(os.Stderr, "Authorized: %s\nSession: %s\n", name, c.sessFile())

		fc := readFileConfig()
		if fc.DefaultAccount == "" {
			fc.DefaultAccount = phone
			if err := writeFileConfig(fc); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not set default account: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "Set as default account")
			}
		}
		return printJSON(map[string]any{
			"status":     "authorized",
			"phone":      self.Phone,
			"username":   self.Username,
			"first_name": self.FirstName,
			"last_name":  self.LastName,
		})
	})
}

// cmdAuth is the interactive single-step auth (reads code from stdin).
func cmdAuth(c config, args []string) error {
	phone, password := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--password", "-p":
			if i+1 < len(args) {
				password = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") && phone == "" {
				phone = args[i]
			}
		}
	}
	if phone == "" {
		return fmt.Errorf("usage: tg-cli auth <phone> [--password <2fa>]")
	}

	c.account = phone

	codeAuth := auth.CodeAuthenticatorFunc(func(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
		fmt.Fprint(os.Stderr, "\nEnter verification code from Telegram: ")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return strings.TrimSpace(scanner.Text()), nil
	})

	fmt.Fprintf(os.Stderr, "Connecting to Telegram as %s...\n", phone)

	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		flow := auth.NewFlow(
			auth.Constant(phone, password, codeAuth),
			auth.SendCodeOptions{},
		)
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				return fmt.Errorf("2FA required — re-run: tg-cli auth %s --password <2fa_password>", phone)
			}
			return err
		}

		self, err := client.Self(ctx)
		if err != nil {
			return err
		}

		name := strings.TrimSpace(self.FirstName + " " + self.LastName)
		if self.Username != "" {
			fmt.Fprintf(os.Stderr, "Authorized: %s (@%s)\n", name, self.Username)
		} else {
			fmt.Fprintf(os.Stderr, "Authorized: %s\n", name)
		}
		fmt.Fprintf(os.Stderr, "Session: %s\n", c.sessFile())

		fc := readFileConfig()
		if fc.DefaultAccount == "" {
			fc.DefaultAccount = phone
			if err := writeFileConfig(fc); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not set default account: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "Set as default account")
			}
		} else if fc.DefaultAccount != phone {
			fmt.Fprintf(os.Stderr, "To set as default: tg-cli accounts use %s\n", phone)
		}
		return nil
	})
}

// cmdAuthQR authorizes via QR code (no phone number needed).
// The user scans the QR with their Telegram app: Settings → Devices → Link Desktop Device.
func cmdAuthQR(c config) error {
	// Use a temporary account slot; session will be moved after auth.
	const qrSlot = "_qr_pending"
	c.account = qrSlot

	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		appID, err := c.appIDInt()
		if err != nil {
			return err
		}

		// Export initial QR login token.
		result, err := api.AuthExportLoginToken(ctx, &tg.AuthExportLoginTokenRequest{
			APIID:     appID,
			APIHash:   c.apiHash,
			ExceptIDs: []int64{},
		})
		if err != nil {
			return fmt.Errorf("export token: %w", err)
		}

		switch r := result.(type) {
		case *tg.AuthLoginToken:
			tokenB64 := base64.RawURLEncoding.EncodeToString(r.Token)
			qrURL := "tg://login?token=" + tokenB64
			expiry := time.Unix(int64(r.Expires), 0)

			fmt.Fprintln(os.Stderr, "Open Telegram → Settings → Devices → Link Desktop Device and scan:")
			fmt.Fprintln(os.Stderr)
			printTerminalQR(qrURL)
			fmt.Fprintf(os.Stderr, "\nExpires at %s. Waiting...\n", expiry.Format("15:04:05"))

			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					if time.Now().After(expiry) {
						return fmt.Errorf("QR code expired — run auth-qr again")
					}
					polled, err := api.AuthExportLoginToken(ctx, &tg.AuthExportLoginTokenRequest{
						APIID:     appID,
						APIHash:   c.apiHash,
						ExceptIDs: []int64{},
					})
					if err != nil {
						if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
							return fmt.Errorf("2FA is enabled — QR login with 2FA not supported yet; use: tg-cli auth <phone> --password <2fa>")
						}
						fmt.Fprintf(os.Stderr, "poll: %v\n", err)
						continue
					}
					switch pr := polled.(type) {
					case *tg.AuthLoginToken:
						expiry = time.Unix(int64(pr.Expires), 0)
					case *tg.AuthLoginTokenSuccess:
						return finishQRAuth(c, pr, qrSlot)
					case *tg.AuthLoginTokenMigrateTo:
						return fmt.Errorf("DC migration to DC %d required — not supported", pr.DCID)
					}
				}
			}

		case *tg.AuthLoginTokenSuccess:
			return finishQRAuth(c, r, qrSlot)

		case *tg.AuthLoginTokenMigrateTo:
			return fmt.Errorf("DC migration to DC %d required — not supported", r.DCID)
		}
		return nil
	})
}

// finishQRAuth extracts user info from a successful QR login and relocates the session.
func finishQRAuth(c config, success *tg.AuthLoginTokenSuccess, qrSlot string) error {
	authAuth, ok := success.Authorization.(*tg.AuthAuthorization)
	if !ok {
		return fmt.Errorf("unexpected authorization type: %T", success.Authorization)
	}
	u, ok := authAuth.User.(*tg.User)
	if !ok {
		return fmt.Errorf("unexpected user type: %T", authAuth.User)
	}

	phone := u.Phone
	if phone != "" && !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if phone == "" || phone == "+" {
		phone = fmt.Sprintf("id_%d", u.ID)
	}

	// Move session from _qr_pending to proper account dir.
	oldDir := c.accountDir()
	newC := c
	newC.account = phone
	newDir := newC.accountDir()

	if oldDir != newDir {
		if err := os.MkdirAll(newDir, 0700); err == nil {
			oldSess := filepath.Join(oldDir, "session.json")
			newSess := filepath.Join(newDir, "session.json")
			if err := os.Rename(oldSess, newSess); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not move session to %s: %v\n", newSess, err)
			} else {
				os.Remove(oldDir)
			}
		}
	}

	fc := readFileConfig()
	if fc.DefaultAccount == "" {
		fc.DefaultAccount = phone
		if err := writeFileConfig(fc); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not set default account: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "Set as default account")
		}
	}

	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	fmt.Fprintf(os.Stderr, "Authorized via QR: %s (%s)\n", name, phone)

	return printJSON(map[string]any{
		"status":     "authorized",
		"id":         u.ID,
		"phone":      u.Phone,
		"username":   u.Username,
		"first_name": u.FirstName,
		"last_name":  u.LastName,
	})
}

// printTerminalQR renders a QR code for the given text as Unicode block characters to stderr.
// Each module is represented by two characters to keep the QR roughly square.
// Works best on dark-background terminals; if scanning fails, use the raw URL.
func printTerminalQR(text string) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		fmt.Fprintf(os.Stderr, "QR URL: %s\n", text)
		return
	}
	size := code.Size
	border := 2
	for y := -border; y < size+border; y++ {
		for x := -border; x < size+border; x++ {
			if x >= 0 && y >= 0 && x < size && y < size && code.Black(x, y) {
				fmt.Fprint(os.Stderr, "██")
			} else {
				fmt.Fprint(os.Stderr, "  ")
			}
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "If scanning fails, open this URL:\n%s\n", text)
}

func cmdStatus(c config) error {
	if c.account == "" {
		return fmt.Errorf("no accounts — run: tg-cli auth <phone>")
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("session invalid or expired: %w", err)
		}
		name := strings.TrimSpace(self.FirstName + " " + self.LastName)
		fmt.Fprintf(os.Stderr, "Account:  %s\nSession:  %s\n\n", c.account, c.sessFile())
		return printJSON(map[string]any{
			"id":         self.ID,
			"phone":      self.Phone,
			"username":   self.Username,
			"first_name": self.FirstName,
			"last_name":  self.LastName,
			"name":       name,
		})
	})
}
