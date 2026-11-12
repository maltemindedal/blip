package mutation

// mutants is the list of bugs the guard tests in this module must catch. Each
// one is a past mistake, replayed against today's code: the harness applies its
// edits to a copy of the module and runs the tests in Kills, each of which must
// fail. Name the commit that introduced the bug, or the one that added the test
// when the bug was found by hand.
//
// An edit whose Old text no longer appears exactly once fails the harness, so a
// refactor that moves the guarded code has to move its mutant too. To add a
// mutant for a new guard test, see "Guard tests and mutants" in
// docs/guides/testing.md.
var mutants = []mutant{
	{
		Name:   "resolveConfig drops a field added to Config",
		Origin: "69d8b2d added a test that passed this mutant; 4bc4afd fixed it",
		Edits: []edit{
			{
				File: "internal/server/config.go",
				Old:  "\tRateLimit      RateLimitConfig\n}",
				New:  "\tRateLimit      RateLimitConfig\n\tMutantField    int\n}",
			},
			{
				File: "internal/server/config.go",
				Old:  "\t\tresolved = *cfg\n",
				New: "\t\tresolved = Config{Port: cfg.Port, AllowedOrigins: cfg.AllowedOrigins, " +
					"MaxMessageSize: cfg.MaxMessageSize, RateLimit: cfg.RateLimit}\n",
			},
		},
		Kills: []string{"TestResolveConfigPreservesEveryField"},
	},
	{
		Name:   "writeFrame re-reads the queue depth on every pass",
		Origin: "269c99f wrote the loop; e2526d2 added the test that kills this mutant",
		Edits: []edit{{
			File: "internal/server/client.go",
			Old:  "\tfor range len(queued) {\n",
			New:  "\tfor len(queued) > 0 {\n",
		}},
		Kills: []string{"TestWriteFrameLeavesLaterMessagesForTheNextFrame"},
	},
	{
		Name:   "a package-level var calls the clock seam",
		Origin: "c36deb0 added TestClockSeamIsTestOnly; a package-level var passed it until 4bc4afd",
		Edits: []edit{{
			File: "internal/server/zz_mutant_probe.go",
			New:  "package server\n\nimport \"time\"\n\nvar mutantProbe = newRateLimiterAt(1, time.Second, time.Now())\n",
		}},
		Kills: []string{"TestClockSeamIsTestOnly"},
	},
	{
		Name:   "allowAt never advances its baseline",
		Origin: "c36deb0 mutation-checked the limiter baseline; this test pins the refill",
		Edits: []edit{{
			File: "internal/server/rate_limiter.go",
			Old:  "\t\trl.last = now\n",
			New:  "",
		}},
		Kills: []string{"TestRateLimiterRefillsFromElapsedTime"},
	},
	{
		Name:   "a listener failure returns without draining",
		Origin: "a363d8c introduced it; 0c8494d fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), s.shutdown())",
			New:  "return fmt.Errorf(\"http server: %w\", err)",
		}},
		Kills: []string{"TestServeDrainsTheHubWhenTheListenerFails"},
	},
	{
		Name:   "a listener failure drains only the hub",
		Origin: "0c8494d left it; d3a9bfe fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), s.shutdown())",
			New:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), withStageDeadline(context.Background(), s.hub.shutdown))",
		}},
		Kills: []string{"TestServeClosesOpenConnectionsWhenTheListenerFails"},
	},
	{
		Name:   "a listener failure that races cancellation is dropped",
		Origin: "061ffa9 introduced it; f23bbde fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\t\tif serveErr != nil {\n",
			New:  "\t\tif serveErr != nil && false {\n",
		}},
		Kills: []string{"TestServeReportsAListenerFailureThatRacesCancellation"},
	},
}
