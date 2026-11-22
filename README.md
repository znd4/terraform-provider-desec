# terraform-provider-desec

An OpenTofu provider for [deSEC](https://desec.io). It manages domains,
RRsets, API tokens and token policies. It uses the Terraform Plugin
Framework.

The provider is not in a registry. It is published as an OCI image at
`ghcr.io/znd4/opentofu-provider-desec`, and OpenTofu installs it through an
`oci_mirror`.

## Why another deSEC provider

[Valodim/desec](https://github.com/Valodim/terraform-provider-desec) exists.
It has `desec_domain`, `desec_rrset`, `desec_token` and `desec_token_policy`.
We do not use it for these reasons:

- Its `desec_token` loses the secret. deSEC returns the secret only when it
  creates the token. The resource sets the secret from each read, so the
  first refresh writes an empty value (issue #23).
- Its `desec_token_policy` reads an empty string as null. You cannot write a
  policy for the zone apex (`subname = ""`).
- It uses the older SDKv2, not the Plugin Framework.

## Install

You need OpenTofu 1.10 or later. Use 1.13 or later if you want
`tofu providers lock -oci-mirror`.

1. Add this to your CLI configuration file:

   ```hcl
   provider_installation {
     oci_mirror {
       repository_template = "ghcr.io/znd4/opentofu-provider-${type}"
       include             = ["registry.opentofu.org/znd4/*"]
     }
     direct {
       exclude = ["registry.opentofu.org/znd4/*"]
     }
   }
   ```

2. Set `TF_CLI_CONFIG_FILE` to the path of that file.
3. Add the provider to your module:

   ```hcl
   terraform {
     required_providers {
       desec = {
         source  = "znd4/desec"
         version = "~> 0.1"
       }
     }
   }
   ```

4. Run `tofu init`.

## Configure

```hcl
provider "desec" {
  # Optional. The default is the DESEC_TOKEN environment variable.
  # token = "..."
}
```

| Attribute | Environment variable | Default |
| --- | --- | --- |
| `token` | `DESEC_TOKEN` | none |
| `api_url` | `DESEC_API_URL` | `https://desec.io/api/v1` |

To manage tokens and policies, the provider token needs
`perm_manage_tokens`. To create and delete domains, it needs
`perm_create_domain` and `perm_delete_domain`.

The client retries a request when deSEC returns `429 Too Many Requests`. It
waits for the time in the `Retry-After` header.

## Resources

### desec_domain

```hcl
resource "desec_domain" "acme" {
  name = "acme.example.com"
}
```

deSEC adds the NS records of its name servers. The `ds` attribute has the DS
records for the parent zone. If you delete the resource, deSEC deletes the
zone and all its records.

Import: `tofu import desec_domain.acme acme.example.com`.

### desec_rrset

```hcl
resource "desec_rrset" "www" {
  domain  = desec_domain.acme.name
  subname = "www"   # omit for the zone apex
  type    = "CNAME"
  ttl     = 3600
  records = ["target.example.net."]
}
```

- Put TXT values in double quotes: `records = ["\"hello\""]`.
- Put a final dot after names in CNAME, MX, NS and SRV values.
- The TTL must be at least the `minimum_ttl` of the domain.

deSEC can store a value in another form, for example `2001:db8::1` for
`2001:db8:0::1`. The provider keeps your form in the state. A later change
outside of OpenTofu still shows in the plan.

Import ID: `<domain>/<subname>/<type>`. Use `@` for the apex:
`tofu import desec_rrset.txt acme.example.com/@/TXT`.

### desec_token

```hcl
resource "desec_token" "acme" {
  name = "hl01-acme"
}

output "acme_token" {
  value     = desec_token.acme.token
  sensitive = true
}
```

Optional attributes: `perm_create_domain`, `perm_delete_domain`,
`perm_manage_tokens`, `auto_policy` (all default `false`) and
`allowed_subnets`.

The `token` attribute is the secret. deSEC returns it only once, when it
creates the token. The provider keeps it in the state and never reads it
again. **Encrypt the state.**

Import: `tofu import desec_token.acme <token id>`. After an import, `token`
is null.

The provider does not manage `max_age` or `max_unused_period` yet.

### desec_token_policy

A token with policies can write only the RRsets that a policy allows. Every
token can read all RRsets.

deSEC has an order rule. The default policy (domain, subname and type all
null) must exist before any other policy of the token. deSEC deletes it last.
Use `depends_on`, so OpenTofu follows this order on create and on destroy:

```hcl
resource "desec_token_policy" "default" {
  token_id = desec_token.acme.id
  # domain, subname and type are null: this is the default policy.
  # perm_write defaults to false: no write access.
}

resource "desec_token_policy" "challenge" {
  token_id   = desec_token.acme.id
  domain     = "acme.example.com"
  subname    = "_acme-challenge"
  type       = "TXT"
  perm_write = true
  depends_on = [desec_token_policy.default]
}
```

A null field matches all values. An empty `subname` is the zone apex.

Import ID: `<token id>/<policy id>`.

## Develop

1. Install Go and OpenTofu.
2. Run the tests:

   ```sh
   go test ./...
   ```

   The resource tests run the provider through `tofu` against an in-memory
   fake API (`internal/desec/fake`). They need no network access. They skip
   if no `tofu` or `terraform` is on `PATH`.

3. Run the acceptance tests only against an account that you can clean up.
   They make and delete real tokens and domains:

   ```sh
   TF_ACC=1 DESEC_TOKEN=... DESEC_ACC_DOMAIN_SUFFIX=example.com go test ./internal/provider -run TestAcc
   ```

   Without `DESEC_ACC_DOMAIN_SUFFIX`, the domain test skips.

## Release

1. Push a tag `v<semver>`, for example `v0.1.0`.
2. The `release` workflow builds the zips with GoReleaser and makes a GitHub
   release.
3. The workflow then runs the `publish-oci` workflow. It downloads the zips
   from the release and runs `scripts/publish-oci.sh`. The script pushes them
   to `ghcr.io/znd4/opentofu-provider-desec:<version>`. The OCI tag has no
   `v`.

To push an existing release again, run the `publish-oci` workflow by hand:

```sh
gh workflow run publish-oci.yaml -f tag=v0.1.0
```

The OCI layout follows the
[OpenTofu provider mirror docs](https://opentofu.org/docs/cli/oci_registries/provider-mirror/):

- The tag points to an image index with
  `artifactType: application/vnd.opentofu.provider`.
- The index has one manifest per platform, with
  `artifactType: application/vnd.opentofu.provider-target` and a `platform`.
- Each manifest has one `archive/zip` layer: the GoReleaser zip.

ORAS 1.3.0 or later writes this layout. The script checks the index before
the push.

To check the layout without a push:

```sh
goreleaser release --snapshot --clean
DRY_RUN=1 scripts/publish-oci.sh 0.0.0-SNAPSHOT-none ghcr.io/znd4/opentofu-provider-desec
```

After the first release, make the package public one time. Open the package
settings on GitHub and set the visibility to public. Otherwise anonymous
pulls fail.
