package main

import (
	"fmt"
	ocrcatalog "github.com/lehigh-university-libraries/scribe/config"
	"sort"
)

func main() {
	models := ocrcatalog.RecognitionModels()
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := models[id]
		fmt.Printf("%s\t%s\t%s\t%s\n", id, m.DOI, m.File, m.SHA256)
	}
}
