package mutation

// notYetMutated lists the guard tests that predate the harness. It may only shrink.
var notYetMutated = map[string]bool{
	"TestHubBroadcastReachesEveryOtherClient":        true,
	"TestHubBroadcastSkipsTheSender":                 true,
	"TestHubDropsClientWithAFullInbox":               true,
	"TestHubRejectsClientWorkAfterShutdown":          true,
	"TestHubUnregisterOfAGoneClientIsANoOp":          true,
	"TestHubsCarryTheirOwnOriginPolicy":              true,
	"TestNewConfigFromEnvMaxMessageSize":             true,
	"TestNewConfigFromEnvRateLimit":                  true,
	"TestNewConfigFromEnvWarnsOnInvalidValues":       true,
	"TestNewConfiguresHTTPServer":                    true,
	"TestNewUpgraderAppliesTheHubsSettings":          true,
	"TestNormalizeMessageMatchesEncodingJSON":        true,
	"TestOriginPolicyAllows":                         true,
	"TestRateLimitWarnsOncePerEpisode":               true,
	"TestRateLimiterCapsRefillAtCapacity":            true,
	"TestRateLimiterGrantsNothingWithoutElapsedTime": true,
	"TestRateLimiterIgnoresBackwardsClock":           true,
	"TestRateLimiterRestoresBurstAfterOneInterval":   true,
	"TestRunStartsNothingWhenItCannotListen":         true,
	"TestServeClosesItsListenerBeforeReturning":      true,
	"TestWriteFrameCoalescesTheQueue":                true,
	"TestWriteFrameDoesNotWaitOnAnEmptyQueue":        true,
	"TestWriteFrameStopsAtTheFirstWriteError":        true,
	"TestZeroValueRateLimiterAllows":                 true,
}
