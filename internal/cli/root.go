package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/theworkflowco/pp-substack/internal/markdown"
	"github.com/theworkflowco/pp-substack/internal/substack"
)

const requestTimeout = 30 * time.Second

var publicationSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Service interface {
	CreateDraft(
		ctx context.Context,
		title string,
		proseMirrorBody string,
		correlationMarker string,
	) (substack.Draft, error)
	UpdateDraft(
		ctx context.Context,
		postID string,
		title string,
		proseMirrorBody string,
		correlationMarker string,
	) (substack.UpdatedDraft, error)
	FindByMarker(ctx context.Context, correlationMarker string) (substack.Found, error)
	GetPost(ctx context.Context, postID string) (substack.Found, error)
}

type Options struct {
	Version    string
	LookupEnv  func(string) (string, bool)
	ReadFile   func(string) ([]byte, error)
	NewService func(publication string, cookie string) (Service, error)
}

func NewRoot(options Options) *cobra.Command {
	options = withDefaults(options)
	root := &cobra.Command{
		Use:           "pp-substack",
		Short:         "Create, update, and reconcile Substack newsletter drafts",
		Args:          rejectPositionalArguments,
		RunE:          showHelp,
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error {
		return usageError("invalid command flags")
	})
	root.AddCommand(newVersionCommand(options))

	drafts := &cobra.Command{
		Use:   "drafts",
		Short: "Create, find, or update newsletter drafts",
		Args:  rejectPositionalArguments,
		RunE:  showHelp,
	}
	drafts.AddCommand(newDraftCreateCommand(options))
	drafts.AddCommand(newDraftFindCommand(options))
	drafts.AddCommand(newDraftUpdateCommand(options))
	root.AddCommand(drafts)

	posts := &cobra.Command{
		Use:   "posts",
		Short: "Read normalized post lifecycle state",
		Args:  rejectPositionalArguments,
		RunE:  showHelp,
	}
	posts.AddCommand(newPostGetCommand(options))
	root.AddCommand(posts)
	return root
}

func newVersionCommand(options Options) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:     "version",
		Short:   "Print the pp-substack version",
		Example: "  pp-substack version --json",
		Args:    rejectPositionalArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			if !asJSON {
				return usageError("--json is required")
			}
			return printJSON(command, struct {
				Version string `json:"version"`
			}{Version: options.Version})
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Write the stable JSON response contract")
	return command
}

func newDraftCreateCommand(options Options) *cobra.Command {
	var publication string
	var title string
	var markdownFile string
	var correlationMarker string
	var asJSON bool
	command := &cobra.Command{
		Use:   "create",
		Short: "Create a newsletter draft without scheduling or sending it",
		Args:  rejectPositionalArguments,
		Example: "  pp-substack drafts create --publication gtmengineersearch " +
			"--title \"GTM jobs this week\" --markdown-file ./issue.md " +
			"--correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d --json",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateJSONAndPublication(asJSON, publication); err != nil {
				return err
			}
			if strings.TrimSpace(title) == "" {
				return requiredFlag("title")
			}
			if strings.TrimSpace(markdownFile) == "" {
				return requiredFlag("markdown-file")
			}
			if strings.TrimSpace(correlationMarker) == "" {
				return requiredFlag("correlation-marker")
			}
			if err := substack.ValidateCorrelationMarker(correlationMarker); err != nil {
				return usageError(err.Error())
			}
			service, err := authenticatedService(options, publication)
			if err != nil {
				return err
			}

			source, err := options.ReadFile(markdownFile)
			if err != nil {
				return fmt.Errorf("read --markdown-file: %w", err)
			}
			body, err := markdown.ToProseMirror(string(source), correlationMarker)
			if err != nil {
				return fmt.Errorf("convert --markdown-file: %w", err)
			}
			result, err := service.CreateDraft(
				command.Context(),
				title,
				body,
				correlationMarker,
			)
			if err != nil {
				return err
			}
			return printJSON(command, result)
		},
	}
	command.Flags().StringVar(&publication, "publication", "", "Substack publication slug")
	command.Flags().StringVar(&title, "title", "", "Newsletter draft title")
	command.Flags().StringVar(&markdownFile, "markdown-file", "", "Path to the rendered Markdown issue")
	command.Flags().StringVar(
		&correlationMarker,
		"correlation-marker",
		"",
		"Deterministic gtme-issue:<uuid> reconciliation marker",
	)
	command.Flags().BoolVar(&asJSON, "json", false, "Write the stable JSON response contract")
	return command
}

func newDraftUpdateCommand(options Options) *cobra.Command {
	var publication string
	var postID string
	var title string
	var markdownFile string
	var correlationMarker string
	var asJSON bool
	command := &cobra.Command{
		Use:   "update",
		Short: "Update the title and body of an unscheduled, unpublished draft",
		Args:  rejectPositionalArguments,
		Example: "  pp-substack drafts update --publication gtmengineersearch " +
			"--post-id 208706412 --title \"Updated GTM jobs this week\" " +
			"--markdown-file ./issue.md " +
			"--correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d --json",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateJSONAndPublication(asJSON, publication); err != nil {
				return err
			}
			if strings.TrimSpace(postID) == "" {
				return requiredFlag("post-id")
			}
			if strings.TrimSpace(title) == "" {
				return requiredFlag("title")
			}
			if strings.TrimSpace(markdownFile) == "" {
				return requiredFlag("markdown-file")
			}
			if strings.TrimSpace(correlationMarker) == "" {
				return requiredFlag("correlation-marker")
			}
			if err := substack.ValidateCorrelationMarker(correlationMarker); err != nil {
				return usageError(err.Error())
			}
			service, err := authenticatedService(options, publication)
			if err != nil {
				return err
			}

			source, err := options.ReadFile(markdownFile)
			if err != nil {
				return fmt.Errorf("read --markdown-file: %w", err)
			}
			body, err := markdown.ToProseMirror(string(source), correlationMarker)
			if err != nil {
				return fmt.Errorf("convert --markdown-file: %w", err)
			}
			result, err := service.UpdateDraft(
				command.Context(),
				postID,
				title,
				body,
				correlationMarker,
			)
			if err != nil {
				return err
			}
			return printJSON(command, result)
		},
	}
	command.Flags().StringVar(&publication, "publication", "", "Substack publication slug")
	command.Flags().StringVar(&postID, "post-id", "", "External Substack draft ID")
	command.Flags().StringVar(&title, "title", "", "Newsletter draft title")
	command.Flags().StringVar(&markdownFile, "markdown-file", "", "Path to the rendered Markdown issue")
	command.Flags().StringVar(
		&correlationMarker,
		"correlation-marker",
		"",
		"Deterministic gtme-issue:<uuid> reconciliation marker",
	)
	command.Flags().BoolVar(&asJSON, "json", false, "Write the stable JSON response contract")
	return command
}

func newDraftFindCommand(options Options) *cobra.Command {
	var publication string
	var correlationMarker string
	var asJSON bool
	command := &cobra.Command{
		Use:   "find",
		Short: "Find exactly one post by its reconciliation marker",
		Args:  rejectPositionalArguments,
		Example: "  pp-substack drafts find --publication gtmengineersearch " +
			"--correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d --json",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateJSONAndPublication(asJSON, publication); err != nil {
				return err
			}
			if strings.TrimSpace(correlationMarker) == "" {
				return requiredFlag("correlation-marker")
			}
			if err := substack.ValidateCorrelationMarker(correlationMarker); err != nil {
				return usageError(err.Error())
			}
			service, err := authenticatedService(options, publication)
			if err != nil {
				return err
			}
			result, err := service.FindByMarker(command.Context(), correlationMarker)
			if err != nil {
				return err
			}
			return printJSON(command, result)
		},
	}
	command.Flags().StringVar(&publication, "publication", "", "Substack publication slug")
	command.Flags().StringVar(
		&correlationMarker,
		"correlation-marker",
		"",
		"Deterministic gtme-issue:<uuid> reconciliation marker",
	)
	command.Flags().BoolVar(&asJSON, "json", false, "Write the stable JSON response contract")
	return command
}

func newPostGetCommand(options Options) *cobra.Command {
	var publication string
	var postID string
	var asJSON bool
	command := &cobra.Command{
		Use:     "get",
		Short:   "Get strict draft, scheduled, or published lifecycle state",
		Example: "  pp-substack posts get --publication gtmengineersearch --post-id 208706412 --json",
		Args:    rejectPositionalArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateJSONAndPublication(asJSON, publication); err != nil {
				return err
			}
			if strings.TrimSpace(postID) == "" {
				return requiredFlag("post-id")
			}
			service, err := authenticatedService(options, publication)
			if err != nil {
				return err
			}
			result, err := service.GetPost(command.Context(), postID)
			if err != nil {
				return err
			}
			return printJSON(command, result)
		},
	}
	command.Flags().StringVar(&publication, "publication", "", "Substack publication slug")
	command.Flags().StringVar(&postID, "post-id", "", "External Substack post ID")
	command.Flags().BoolVar(&asJSON, "json", false, "Write the stable JSON response contract")
	return command
}

func rejectPositionalArguments(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError("positional arguments are not accepted")
	}
	return nil
}

func showHelp(command *cobra.Command, _ []string) error {
	return command.Help()
}

func validateJSONAndPublication(asJSON bool, publication string) error {
	if !asJSON {
		return usageError("--json is required")
	}
	if !publicationSlugPattern.MatchString(publication) {
		return usageError(
			"--publication must be a lowercase Substack slug such as gtmengineersearch",
		)
	}
	return nil
}

func authenticatedService(
	options Options,
	publication string,
) (Service, error) {
	cookie, ok := options.LookupEnv("PP_SUBSTACK_SESSION_COOKIE")
	if !ok || strings.TrimSpace(cookie) == "" {
		return nil, authError("PP_SUBSTACK_SESSION_COOKIE is required")
	}
	service, err := options.NewService(publication, cookie)
	if err != nil {
		return nil, err
	}
	return service, nil
}

func printJSON(command *cobra.Command, value any) error {
	encoder := json.NewEncoder(command.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func withDefaults(options Options) Options {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.LookupEnv == nil {
		options.LookupEnv = os.LookupEnv
	}
	if options.ReadFile == nil {
		options.ReadFile = os.ReadFile
	}
	if options.NewService == nil {
		options.NewService = func(publication string, cookie string) (Service, error) {
			return substack.NewClient(
				"https://"+publication+".substack.com",
				"https://substack.com",
				cookie,
				&http.Client{Timeout: requestTimeout},
			)
		}
	}
	return options
}
