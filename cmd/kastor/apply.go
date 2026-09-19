package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/getkastordev/kastor/internal/provider"
)

func newApplyCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "apply [--target name] [dir]",
		Short: "Reconcile platform targets and update the state file",
		Long:  "apply plans each platform target (exactly like kastor plan) and then executes the changes: create, update, and delete remote resources until the platform matches the spec. State is saved after every completed operation, so an interrupted apply loses nothing — re-running plans only the remainder. apply does not prompt for confirmation in v0.",
		Args:  usageMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runApply(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), dir, target, false)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "apply only the named platform target (default: all platform targets)")
	return cmd
}

func newDestroyCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "destroy [--target name] [dir]",
		Short: "Delete every remote resource the state file tracks",
		Long:  "destroy deletes all managed remote resources of the module's platform targets, in reverse dependency order, and removes them from the state file. Like apply, state is saved after every deletion. destroy does not prompt for confirmation in v0.",
		Args:  usageMaxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runApply(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), dir, target, true)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "destroy only the named platform target (default: all platform targets)")
	return cmd
}

// runApply reconciles (or destroys) each selected platform target: plan,
// render, execute. State is persisted through the save closure after every
// operation; state write failures are IO errors (exit 2), and the engine's
// error already tells the user what was applied before the failure.
func runApply(ctx context.Context, stdout, stderr io.Writer, dir, targetName string, destroy bool) error {
	jobs, release, err := preparePlatform(ctx, stderr, dir, targetName)
	if err != nil {
		return err
	}
	defer releaseAndWarn(stderr, release)

	for i, pj := range jobs {
		if i > 0 {
			io.WriteString(stdout, "\n")
		}

		var plan *provider.Plan
		if destroy {
			plan, err = provider.BuildDestroyPlan(pj.job)
		} else {
			plan, err = provider.BuildPlan(ctx, pj.provider, pj.job)
		}
		if err != nil {
			return err
		}

		save := func() error {
			if err := pj.job.State.Write(dir); err != nil {
				return withExitCode(2, err)
			}
			*pj.metadataPending = false
			return nil
		}

		name := pj.job.Target.Name
		if destroy {
			if len(plan.Changes) == 0 {
				fmt.Fprintf(stdout, "Nothing to destroy for target.%s: the state tracks no resources.\n", name)
				continue
			}
			for _, c := range plan.Changes {
				fmt.Fprintf(stdout, "  - %s\n", c.Addr)
			}
			applied, err := provider.Apply(ctx, pj.provider, pj.job, plan, save)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "\nDestroyed target.%s: %d deleted.\n", name, applied)
			continue
		}

		renderPlan(stdout, plan)
		if _, err := provider.Apply(ctx, pj.provider, pj.job, plan, save); err != nil {
			return err
		}
		// A successful no-op apply can commit only ownership metadata. Plan
		// and doctor never reach this mutation path, nor does a failed apply.
		if *pj.metadataPending {
			if err := save(); err != nil {
				return err
			}
		}
		if create, update, del, _ := plan.Counts(); create+update+del > 0 {
			fmt.Fprintf(stdout, "\nApplied target.%s: %d created, %d updated, %d deleted.\n", name, create, update, del)
		}
	}
	return nil
}
