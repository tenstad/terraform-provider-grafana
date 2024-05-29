package generate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/grafana/terraform-provider-grafana/v3/internal/common"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

var (
	allowedTerraformChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

func Generate(ctx context.Context, cfg *Config) error {
	if _, err := os.Stat(cfg.OutputDir); err == nil && cfg.Clobber {
		log.Printf("Deleting all files in %s", cfg.OutputDir)
		if err := os.RemoveAll(cfg.OutputDir); err != nil {
			return fmt.Errorf("failed to delete %s: %s", cfg.OutputDir, err)
		}
	} else if err == nil && !cfg.Clobber {
		return fmt.Errorf("output dir %q already exists. Use the clobber option to delete it", cfg.OutputDir)
	}

	log.Printf("Generating resources to %s", cfg.OutputDir)
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %s", cfg.OutputDir, err)
	}

	// Generate provider installation block
	providerBlock := hclwrite.NewBlock("terraform", nil)
	requiredProvidersBlock := hclwrite.NewBlock("required_providers", nil)
	requiredProvidersBlock.Body().SetAttributeValue("grafana", cty.ObjectVal(map[string]cty.Value{
		"source":  cty.StringVal("grafana/grafana"),
		"version": cty.StringVal(strings.TrimPrefix(cfg.ProviderVersion, "v")),
	}))
	providerBlock.Body().AppendBlock(requiredProvidersBlock)
	if err := writeBlocks(filepath.Join(cfg.OutputDir, "provider.tf"), providerBlock); err != nil {
		log.Fatal(err)
	}

	// Terraform init to download the provider
	if err := runTerraform(cfg.OutputDir, "init"); err != nil {
		return fmt.Errorf("failed to run terraform init: %w", err)
	}

	if cfg.Cloud != nil {
		stacks, err := generateCloudResources(ctx, cfg)
		if err != nil {
			return err
		}

		for _, stack := range stacks {
			if err := generateGrafanaResources(ctx, stack.managementKey, stack.url, "stack-"+stack.slug, false, cfg.OutputDir, stack.smURL, stack.smToken, cfg.IncludeResources); err != nil {
				return err
			}
		}
	}

	if cfg.Grafana != nil {
		if err := generateGrafanaResources(ctx, cfg.Grafana.Auth, cfg.Grafana.URL, "", true, cfg.OutputDir, "", "", cfg.IncludeResources); err != nil {
			return err
		}
	}

	if cfg.Format == OutputFormatJSON {
		return convertToTFJSON(cfg.OutputDir)
	}
	if cfg.Format == OutputFormatCrossplane {
		return errors.New("crossplane output format is not yet supported")
	}

	return nil
}

func generateImportBlocks(ctx context.Context, client *common.Client, listerData any, resources []*common.Resource, outPath, provider string, includedResources []string) error {
	generatedFilename := func(suffix string) string {
		if provider == "" {
			return filepath.Join(outPath, suffix)
		}

		return filepath.Join(outPath, provider+"-"+suffix)
	}

	resources, err := filterResources(resources, includedResources)
	if err != nil {
		return err
	}

	// Generate HCL blocks in parallel with a wait group
	wg := sync.WaitGroup{}
	wg.Add(len(resources))
	type result struct {
		resource *common.Resource
		blocks   []*hclwrite.Block
		err      error
	}
	results := make(chan result, len(resources))

	for _, resource := range resources {
		go func(resource *common.Resource) {
			lister := resource.ListIDsFunc
			if lister == nil {
				log.Printf("skipping %s because it does not have a lister\n", resource.Name)
				wg.Done()
				results <- result{
					resource: resource,
				}
				return
			}

			log.Printf("generating %s resources\n", resource.Name)
			ids, err := lister(ctx, client, listerData)
			if err != nil {
				wg.Done()
				results <- result{
					resource: resource,
					err:      err,
				}
				return
			}

			// Write blocks like these
			// import {
			//   to = aws_iot_thing.bar
			//   id = "foo"
			// }
			var blocks []*hclwrite.Block
			for _, id := range ids {
				cleanedID := allowedTerraformChars.ReplaceAllString(id, "_")
				if provider != "cloud" {
					cleanedID = strings.ReplaceAll(provider, "-", "_") + "_" + cleanedID
				}

				matched, err := filterResourceByName(resource.Name, cleanedID, includedResources)
				if err != nil {
					wg.Done()
					results <- result{
						resource: resource,
						err:      err,
					}
					return
				}
				if !matched {
					continue
				}

				b := hclwrite.NewBlock("import", nil)
				b.Body().SetAttributeTraversal("to", traversal(resource.Name, cleanedID))
				b.Body().SetAttributeValue("id", cty.StringVal(id))
				if provider != "" {
					b.Body().SetAttributeTraversal("provider", traversal("grafana", provider))
				}

				blocks = append(blocks, b)
			}

			wg.Done()
			results <- result{
				resource: resource,
				blocks:   blocks,
			}
			log.Printf("finished generating blocks for %s resources\n", resource.Name)
		}(resource)
	}

	// Wait for all results
	wg.Wait()
	close(results)

	resultsSlice := []result{}
	for r := range results {
		if r.err != nil {
			return fmt.Errorf("failed to generate %s resources: %w", r.resource.Name, r.err)
		}
		resultsSlice = append(resultsSlice, r)
	}
	sort.Slice(resultsSlice, func(i, j int) bool {
		return resultsSlice[i].resource.Name < resultsSlice[j].resource.Name
	})

	// Collect results
	allBlocks := []*hclwrite.Block{}
	for _, r := range resultsSlice {
		allBlocks = append(allBlocks, r.blocks...)
	}

	if err := writeBlocks(generatedFilename("imports.tf"), allBlocks...); err != nil {
		return err
	}

	if err := runTerraform(outPath, "plan", "-generate-config-out="+generatedFilename("resources.tf")); err != nil {
		return fmt.Errorf("failed to generate resources: %w", err)
	}

	return sortResourcesFile(generatedFilename("resources.tf"))
}

func filterResources(resources []*common.Resource, includedResources []string) ([]*common.Resource, error) {
	if len(includedResources) == 0 {
		return resources, nil
	}

	filteredResources := []*common.Resource{}
	allowedResourceTypes := []string{}
	for _, included := range includedResources {
		if !strings.Contains(included, ".") {
			return nil, fmt.Errorf("included resource %q is not in the format <type>.<name>", included)
		}
		allowedResourceTypes = append(allowedResourceTypes, strings.Split(included, ".")[0])
	}

	for _, resource := range resources {
		for _, allowedResourceType := range allowedResourceTypes {
			matched, err := filepath.Match(allowedResourceType, resource.Name)
			if err != nil {
				return nil, err
			}
			if matched {
				filteredResources = append(filteredResources, resource)
				break
			}
		}
	}
	return filteredResources, nil
}

func filterResourceByName(resourceType, resourceName string, includedResources []string) (bool, error) {
	if len(includedResources) == 0 {
		return true, nil
	}

	for _, included := range includedResources {
		matched, err := filepath.Match(included, resourceType+"."+resourceName)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
