package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/aoos/dejima/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// newProviderCmd manages the daemon's account-wide LLM provider credentials:
// (provider → api_key) entries that key-requiring agent frameworks (OpenClaw,
// Letta, …) use to reach a model. Keys are stored once and reused across every
// island whose agents target that provider.
func newProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage LLM provider credentials (account-wide API keys).",
	}
	cmd.AddCommand(newProviderLsCmd(), newProviderSetCmd(), newProviderRmCmd())
	return cmd
}

func newProviderLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configured providers (keys are never shown).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			provs, err := c.ListProviderCredentials(cmd.Context())
			if err != nil {
				return err
			}
			if len(provs) == 0 {
				fmt.Println("no providers configured — add one with `dejima provider set <provider>`")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "PROVIDER\tKEY\tENV VAR\tDEFAULT\tBASE URL")
			for _, p := range provs {
				keyCol := "not set"
				if p.KeySet {
					keyCol = "set " + p.Hint
				}
				def := ""
				if p.Default {
					def = "✓"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, keyCol, p.EnvVar, def, p.BaseURL)
			}
			return tw.Flush()
		},
	}
}

func newProviderSetCmd() *cobra.Command {
	var key, keyFile, baseURL, envVar string
	var stdin, makeDefault bool
	cmd := &cobra.Command{
		Use:   "set <provider>",
		Short: "Store or update a provider API key (anthropic, openai, …).",
		Long: "Store or update a provider API key on the daemon. With no --key/--key-file/--stdin\n" +
			"flag the key is read from an interactive no-echo prompt (never from argv).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.TrimSpace(args[0])
			apiKey, err := readProviderKey(key, keyFile, stdin)
			if err != nil {
				return err
			}
			if strings.TrimSpace(apiKey) == "" {
				return fmt.Errorf("an API key is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			if _, err := c.PutProviderCredential(cmd.Context(), provider, api.PutProviderCredentialRequest{
				APIKey:  apiKey,
				BaseURL: baseURL,
				EnvVar:  envVar,
				Default: makeDefault,
			}); err != nil {
				return err
			}
			fmt.Printf("stored key for provider %q (applies to every agent that targets %s)\n", provider, provider)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "the API key (avoid — visible in shell history; prefer the prompt or --key-file)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "read the key from this file")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the key from stdin")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "optional endpoint override (proxy/Azure/self-host)")
	cmd.Flags().StringVar(&envVar, "env-var", "", "override the env var name (default UPPER(provider)_API_KEY)")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "make this the default provider")
	return cmd
}

func newProviderRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <provider>",
		Short: "Remove a provider credential.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			affected, err := c.DeleteProviderCredential(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("removed provider %q\n", args[0])
			if len(affected) > 0 {
				fmt.Printf("warning: still referenced by %d island(s): %s\n",
					len(affected), strings.Join(affected, ", "))
				fmt.Println("  those agents will read 'no model key' until reconfigured")
			}
			return nil
		},
	}
}

// readProviderKey resolves the API key from (in priority order) --key, --key-file,
// --stdin, or an interactive no-echo prompt — so a secret never has to be passed
// in argv (where it lands in shell history / the process table).
func readProviderKey(key, keyFile string, stdin bool) (string, error) {
	switch {
	case key != "":
		return key, nil
	case keyFile != "":
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	case stdin:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	default:
		fmt.Print("API key (input hidden): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
}
