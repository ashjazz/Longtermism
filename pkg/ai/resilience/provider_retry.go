package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

const (
	defaultProviderTimeout      = 60 * time.Second
	defaultProviderRetryMax     = 2
	defaultProviderRetryBackoff = time.Second
	maxProviderRetryMax         = 2
)

var providerRetryDelayMultipliers = [...]time.Duration{1, 3}

// ProviderExecutionPolicy is provider-neutral execution policy. It intentionally contains no
// OpenAI/Anthropic fields: adapters own protocol-specific configuration while resilience owns
// timeout, retry, cancellation, and terminal stream semantics.
type ProviderExecutionPolicy struct {
	Timeout      time.Duration
	RetryMax     int
	RetryBackoff time.Duration
}

func DefaultProviderExecutionPolicy() ProviderExecutionPolicy {
	return ProviderExecutionPolicy{
		Timeout:      defaultProviderTimeout,
		RetryMax:     defaultProviderRetryMax,
		RetryBackoff: defaultProviderRetryBackoff,
	}
}

func (p ProviderExecutionPolicy) Validate() error {
	if p.Timeout <= 0 {
		return fmt.Errorf("provider execution timeout must be positive")
	}
	if p.RetryMax < 0 || p.RetryMax > maxProviderRetryMax {
		return fmt.Errorf("provider execution retry limit is invalid")
	}
	if p.RetryBackoff <= 0 {
		return fmt.Errorf("provider execution retry backoff must be positive")
	}
	return nil
}

func retryProviderCall(ctx context.Context, policy ProviderExecutionPolicy, sleep func(context.Context, time.Duration) error, call func(context.Context) error) error {
	for attempt := 0; ; attempt++ {
		err := call(ctx)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !errors.Is(err, llm.ErrUpstream) || attempt >= policy.RetryMax {
			return err
		}
		if err := sleep(ctx, providerRetryDelay(attempt, policy.RetryBackoff)); err != nil {
			return err
		}
	}
}

func providerRetryDelay(attempt int, base time.Duration) time.Duration {
	if attempt >= len(providerRetryDelayMultipliers) {
		return base * providerRetryDelayMultipliers[len(providerRetryDelayMultipliers)-1]
	}
	return base * providerRetryDelayMultipliers[attempt]
}

func providerSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
