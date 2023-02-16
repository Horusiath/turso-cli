package cmd

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"text/template"

	"github.com/chiselstrike/iku-turso-cli/internal/settings"
	"github.com/chiselstrike/iku-turso-cli/internal/turso"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

//go:embed login.html
var LOGIN_HTML string

var authCmd = &cobra.Command{
	Use:               "auth",
	Short:             "Authenticate with Turso",
	ValidArgsFunction: noSpaceArg,
}

var loginCmd = &cobra.Command{
	Use:               "login",
	Short:             "Login to the platform.",
	Args:              cobra.NoArgs,
	ValidArgsFunction: noFilesArg,
	RunE:              login,
}

var logoutCmd = &cobra.Command{
	Use:               "logout",
	Short:             "Log out currently logged in user.",
	Args:              cobra.NoArgs,
	ValidArgsFunction: noFilesArg,
	RunE:              logout,
}

var tokenCmd = &cobra.Command{
	Use:               "token",
	Short:             "Show token used for authorization.",
	Args:              cobra.NoArgs,
	ValidArgsFunction: noFilesArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		settings, err := settings.ReadSettings()
		if err != nil {
			return fmt.Errorf("could not retrieve local config: %w", err)
		}
		token := settings.GetToken()
		if !isJwtTokenValid(token) {
			return fmt.Errorf("no user logged in. Run `turso auth login` to log in and get a token")
		}
		fmt.Println(token)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(loginCmd)
	authCmd.AddCommand(logoutCmd)
	authCmd.AddCommand(tokenCmd)
}

func isJwtTokenValid(token string) bool {
	if len(token) == 0 {
		return false
	}
	resp, err := createTursoClient().Get("/v2/validate/token", nil)
	return err == nil && resp.StatusCode == http.StatusOK
}

func login(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	settings, err := settings.ReadSettings()
	if err != nil {
		return fmt.Errorf("could not retrieve local config: %w", err)
	}
	if isJwtTokenValid(settings.GetToken()) {
		fmt.Println("✔  Success! Existing JWT still valid")
		return nil
	}
	fmt.Println("Waiting for authentication...")
	ch := make(chan string, 1)
	server, err := createCallbackServer(ch)
	if err != nil {
		return fmt.Errorf("internal error. Cannot create callback: %w", err)
	}

	port, err := runServer(server)
	if err != nil {
		return fmt.Errorf("internal error. Cannot run authentication server: %w", err)
	}

	err = beginAuth(port)
	if err != nil {
		return fmt.Errorf("internal error. Cannot initiate auth flow: %w", err)
	}

	versionChannel := make(chan string, 1)

	go func() {
		latestVersion, err := fetchLatestVersion()
		if err != nil {
			// On error we just behave as the version check has never happend
			versionChannel <- version
			return
		}
		versionChannel <- latestVersion
	}()

	jwt := <-ch

	err = settings.SetToken(jwt)
	server.Shutdown(context.Background())

	if err != nil {
		return fmt.Errorf("error persisting token on local config: %w", err)
	}

	latestVersion := <-versionChannel

	fmt.Println("✔  Success!")

	if version != latestVersion {

		fmt.Printf("\nFriendly reminder that there's a newer version of %s available.\n", turso.Emph("Turso CLI"))
		fmt.Printf("You're currently using version %s while latest available version is %s.\n", turso.Emph(version), turso.Emph(latestVersion))
		fmt.Printf("Please consider updating to get new features and more stable experience.\n\n")
	}

	return nil
}

func fetchLatestVersion() (string, error) {
	resp, err := createUnauthenticatedTursoClient().Get("/releases/latest", nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error getting latest release: %s", resp.Status)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var versionResp struct {
		Version string `json:"latest"`
	}
	if err := json.Unmarshal(body, &versionResp); err != nil {
		return "", err
	}
	if len(versionResp.Version) == 0 {
		return "", fmt.Errorf("got empty version for latest release")
	}
	return versionResp.Version, nil
}

func beginAuth(port int) error {
	authUrl, err := url.Parse(getHost())
	if err != nil {
		return fmt.Errorf("error parsing auth URL: %w", err)
	}
	authUrl.RawQuery = url.Values{
		"port":     {strconv.Itoa(port)},
		"redirect": {"true"},
	}.Encode()

	err = browser.OpenURL(authUrl.String())
	if err != nil {
		fmt.Printf("Please open the following URL to login: %s\n", turso.Emph(authUrl.String()))
	}

	return nil
}

func createCallbackServer(jwtCh chan string) (*http.Server, error) {
	tmpl, err := template.New("login.html").Parse(LOGIN_HTML)
	if err != nil {
		return nil, fmt.Errorf("could not parse login callback template: %w", err)
	}

	handler := http.NewServeMux()
	handler.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		jwtCh <- q.Get("jwt")

		w.WriteHeader(200)
		tmpl.Execute(w, q.Get("username"))
	})

	return &http.Server{Handler: handler}, nil
}

func runServer(server *http.Server) (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, fmt.Errorf("could not allocate port for http server: %w", err)
	}

	go func() {
		server.Serve(listener)
	}()

	return listener.Addr().(*net.TCPAddr).Port, nil
}

func logout(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	settings, err := settings.ReadSettings()
	if err != nil {
		return fmt.Errorf("could not retrieve local config: %w", err)
	}

	token := settings.GetToken()
	if len(token) == 0 {
		fmt.Println("No user logged in.")
	} else {
		settings.SetToken("")
		fmt.Println("Logged out.")
	}

	return nil
}
