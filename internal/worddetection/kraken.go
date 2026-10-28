package worddetection

import "github.com/lehigh-university-libraries/htr/pkg/segment/legacy"

// KrakenProvider is the shared v1 compatibility adapter. New geometry uses v2.
type KrakenProvider = legacy.KrakenProvider

func NewKraken(model string) *KrakenProvider { return legacy.NewKraken(model) }
