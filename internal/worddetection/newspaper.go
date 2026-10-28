package worddetection

import "github.com/lehigh-university-libraries/htr/pkg/segment/legacy"

type NewspaperProvider = legacy.NewspaperProvider

func NewNewspaper(model string) *NewspaperProvider { return legacy.NewNewspaper(model) }
