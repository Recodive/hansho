// Command terraform-provider-hansho is the Terraform provider for the Hansho hub.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/recodive/hansho/terraform-provider-hansho/internal/provider"
)

func main() {
	debug := flag.Bool("debug", false, "run the provider with support for debuggers")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New, providerserver.ServeOpts{
		Address: "registry.terraform.io/recodive/hansho",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
