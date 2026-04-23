package bot

import (
	"context"
	"math/rand"
	"testing"
)

func TestBuildMathImageChallengeReturnsValidChallengeAcrossSeeds(t *testing.T) {
	for seed := int64(0); seed < 1000; seed++ {
		r := rand.New(rand.NewSource(seed))

		challenge, err := buildMathImageChallenge(context.Background(), r, maxMathImageChallengeAttempts)
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", seed, err)
		}
		if challenge.Answer < 0 {
			t.Fatalf("seed %d: negative answer: %d", seed, challenge.Answer)
		}
		if len(challenge.Options) != 4 {
			t.Fatalf("seed %d: expected 4 options, got %d", seed, len(challenge.Options))
		}

		seen := make(map[int]struct{}, len(challenge.Options))
		foundAnswer := false
		for _, option := range challenge.Options {
			if option < 0 {
				t.Fatalf("seed %d: negative option: %d", seed, option)
			}
			if _, ok := seen[option]; ok {
				t.Fatalf("seed %d: duplicate option: %d", seed, option)
			}
			seen[option] = struct{}{}
			if option == challenge.Answer {
				foundAnswer = true
			}
		}
		if !foundAnswer {
			t.Fatalf("seed %d: answer %d missing from options %v", seed, challenge.Answer, challenge.Options)
		}
	}
}
