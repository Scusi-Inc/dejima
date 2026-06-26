package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newPolicyCmd manages cross-island action auto-approve rules (the action gate's
// policy layer). Default posture is prompt-everything; an operator adds scoped,
// counted, expiring rules as they come to trust an orchestrator. Destructive
// actions are never auto-approved regardless of any rule.
func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Auto-approve rules for cross-island actions (operator).",
		Long: "Scoped, counted, expiring rules that auto-approve a named action over a link\n" +
			"(from→to) — so an orchestrator dispatching many actions isn't a human prompt\n" +
			"each time. Destructive actions are NEVER auto-approved. Default is\n" +
			"prompt-everything; add rules deliberately. Adding/removing a rule is ledgered.",
	}
	cmd.AddCommand(newPolicyAddCmd(), newPolicyLsCmd(), newPolicyRmCmd())
	return cmd
}

// parseLink splits a "--link from->to" value into its two islands.
func parseLink(s string) (from, to string, err error) {
	parts := strings.SplitN(s, "->", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("--link must be <from>-><to> (got %q)", s)
	}
	from, to = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if from == "" || to == "" {
		return "", "", fmt.Errorf("--link must be <from>-><to> (got %q)", s)
	}
	return from, to, nil
}

func newPolicyAddCmd() *cobra.Command {
	var linkArg, action, ttl string
	var max int
	cmd := &cobra.Command{
		Use:   "add --link <from>-><to> --action <type> [--max N] [--ttl 1h]",
		Short: "Add a scoped auto-approve rule (operator; ledgered).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, to, err := parseLink(linkArg)
			if err != nil {
				return err
			}
			if strings.TrimSpace(action) == "" {
				return fmt.Errorf("--action is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			rule, err := c.AddPolicy(cmd.Context(), api.PolicyAddRequest{
				From: from, To: to, Action: action, MaxCount: max, TTL: ttl,
			})
			if err != nil {
				return err
			}
			limit := "unlimited"
			if rule.MaxCount > 0 {
				limit = fmt.Sprintf("%d", rule.MaxCount)
			}
			exp := "no expiry"
			if !rule.ExpiresAt.IsZero() {
				exp = "expires " + rule.ExpiresAt.Format("2006-01-02 15:04 MST")
			}
			fmt.Printf("auto-approve rule added: %s→%s [%s] (max %s, %s)\n", rule.From, rule.To, rule.Action, limit, exp)
			return nil
		},
	}
	cmd.Flags().StringVar(&linkArg, "link", "", "the link as <from>-><to> (required)")
	cmd.Flags().StringVar(&action, "action", "", "action type to auto-approve (required)")
	cmd.Flags().IntVar(&max, "max", 0, "max auto-approvals (0 = unlimited within --ttl)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "expiry as a Go duration (e.g. 1h, 30m); empty = no expiry")
	return cmd
}

func newPolicyLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List active auto-approve rules (operator).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			rules, err := c.ListPolicy(cmd.Context())
			if err != nil {
				return err
			}
			if len(rules) == 0 {
				fmt.Println("no auto-approve rules — cross-island actions are prompt-everything (add one with `dejima policy add`)")
				return nil
			}
			for _, r := range rules {
				limit := "∞"
				if r.MaxCount > 0 {
					limit = fmt.Sprintf("%d/%d", r.Used, r.MaxCount)
				}
				exp := "—"
				if !r.ExpiresAt.IsZero() {
					exp = r.ExpiresAt.Format("2006-01-02 15:04 MST")
				}
				fmt.Printf("%s→%s  [%s]  used %s  expires %s\n", r.From, r.To, r.Action, limit, exp)
			}
			return nil
		},
	}
}

func newPolicyRmCmd() *cobra.Command {
	var linkArg, action string
	cmd := &cobra.Command{
		Use:   "rm --link <from>-><to> --action <type>",
		Short: "Remove an auto-approve rule (operator; ledgered).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, to, err := parseLink(linkArg)
			if err != nil {
				return err
			}
			if strings.TrimSpace(action) == "" {
				return fmt.Errorf("--action is required")
			}
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RemovePolicy(cmd.Context(), from, to, action); err != nil {
				return err
			}
			fmt.Printf("removed auto-approve rule: %s→%s [%s]\n", from, to, action)
			return nil
		},
	}
	cmd.Flags().StringVar(&linkArg, "link", "", "the link as <from>-><to> (required)")
	cmd.Flags().StringVar(&action, "action", "", "action type (required)")
	return cmd
}
