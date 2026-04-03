package main

import (
  "context"
  "fmt"

  "github.com/cloudreve/Cloudreve/v4/application/constants"
  "github.com/cloudreve/Cloudreve/v4/application/dependency"
  "github.com/cloudreve/Cloudreve/v4/inventory"
  "github.com/cloudreve/Cloudreve/v4/pkg/logging"
  "github.com/cloudreve/Cloudreve/v4/pkg/util"
)

func main() {
  util.UseWorkingDir = true
  logger := logging.NewConsoleLogger(logging.LevelDebug)
  dep := dependency.NewDependency(
    dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
    dependency.WithLogger(logger),
    dependency.WithRequiredDbVersion(constants.BackendVersion),
  )
  defer dep.Shutdown(context.Background())

  ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
  ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)
  reloadCtx := context.WithValue(ctx, dependency.ReloadCtx{}, true)
  sp := dep.SettingProvider()
  es := sp.FTSIndexElasticsearch(reloadCtx)
  tk := sp.FTSTikaExtractor(reloadCtx)
  fmt.Printf("enabled=%v index_type=%q extractor_type=%q es_endpoint=%q es_cloud=%q tika_endpoint=%q\n", sp.FTSEnabled(reloadCtx), sp.FTSIndexType(reloadCtx), sp.FTSExtractorType(reloadCtx), es.Endpoint, es.CloudID, tk.Endpoint)
  idx := dep.SearchIndexer(reloadCtx)
  fmt.Printf("search_indexer=%T\n", idx)
  ext := dep.TextExtractor(reloadCtx)
  fmt.Printf("text_extractor=%T\n", ext)
}
