package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vizkid/roar/internal/store"
)

// cmdBind records an Antigravity pane for native delivery. agy gives its
// agent API credentials only to commands its model runs, so agy itself
// must run this. It prints the pane, never the token.
func cmdBind(ctx context.Context, env Env, args []string) error {
	if len(args) != 1 || args[0] != "agy" {
		return usageErr("roar bind agy")
	}
	block, err := bindAgy(ctx, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "bound %s\n", block)
	return nil
}

// bindAgy writes the binding for the agy pane this command runs in. Every
// command that agy runs carries the credentials, so self() calls it too.
func bindAgy(ctx context.Context, env Env) (string, error) {
	conv, addr, token := env.Getenv("ANTIGRAVITY_CONVERSATION_ID"), env.Getenv("ANTIGRAVITY_LS_ADDRESS"), env.Getenv("ANTIGRAVITY_CSRF_TOKEN")
	if conv == "" || addr == "" || token == "" {
		return "", errors.New("not_in_agy: run roar bind agy from Antigravity's own shell tool (ANTIGRAVITY_* unset)")
	}
	session, block := env.Getenv("REX_SESSION"), env.Getenv("REX_BLOCK")
	if session == "" || block == "" {
		return "", errNotInPane
	}
	rc := localRex(env)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	p, err := rc.Process(ctx, session, block)
	if err != nil {
		return "", err
	}
	if p.LastExit != nil || p.Foreground == nil || p.Foreground.Agent() != "agy" {
		return "", errors.New("not_agy_pane: the pane's foreground program is not agy")
	}
	// REX_BLOCK can be inherited from whatever started this process.
	if !ownPane(ctx, rc, session, block) {
		return "", errors.New("inherited_pane: this command does not run under the pane's agy")
	}
	exe := env.Getenv("ANTIGRAVITY_AGENTAPI_EXE")
	if exe == "" {
		exe = p.Foreground.Path
	}
	st, err := openStore(env)
	if err != nil {
		return "", err
	}
	b := store.AgyBinding{Conversation: conv, Address: addr, Token: token, Exe: exe, Session: session, PID: p.Foreground.PID, At: env.Now()}
	return block, st.BindAgy(block, b)
}
