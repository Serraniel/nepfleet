# Sign-on

One identity provider for the fleet, so users sign in to every app with the same account. NepFleet registers an OpenID Connect client for each stack in the provider and hands the app its issuer, client ID and secret. Nobody sets up clients by hand.

The provider is a stack in the same fleet. [Pocket ID](https://pocket-id.org) is the first one NepFleet supports (version 2.19 or newer). Authelia is planned.

## Setting it up

1. **Deploy the provider.** Create a Pocket ID stack from the `pocket-id` template and apply it. Its admin comes from the stack's inputs, and it generates `STATIC_API_KEY`, which NepFleet uses for Pocket ID's admin API.
2. **Name it in `fleet.yaml`:**

   ```yaml
   spec:
     signOn:
       provider: pocket-id   # the stack's name
   ```

   The app does the same under Features, Sign-on, and sets a stack's mode on its page ([API](api.md): `/api/v1/signon`, `/api/v1/stacks/{name}/signon`).

3. **Apply the stacks.** Every stack whose template supports sign-on now uses it. Their plans show the client they get.

## Per stack: integrated, custom or none

`stack.yaml` says how a stack's users sign in:

```yaml
spec:
  signOn:
    mode: integrated   # integrated | custom | none
```

- **integrated:** a client in the fleet's provider, made and kept by NepFleet. It's the default when `fleet.yaml` names a provider and the template supports sign-on.
- **custom:** a provider of your own, outside the fleet. Set its issuer and the client ID here, and the client secret with `nepfleet secret set <stack> NEPFLEET_OIDC_CLIENT_SECRET`:

  ```yaml
  spec:
    signOn: {mode: custom, issuer: https://login.example.test, clientID: wiki}
  ```

- **none:** the app's own accounts only. It's the default without a provider.

Either way the app gets its settings the same way, from the `NEPFLEET_OIDC_*` built-ins, so a template wires sign-on once.

## What apply does

For an integrated stack:

- **Client:** its ID and name are the stack's name. Its callback is the stack's URL with the template's path, such as `https://wiki.example.test/auth/callback`, and its description says NepFleet manages it.
- **Secret:** generated once, stored encrypted in the stack's `values.secret.yaml` as `NEPFLEET_OIDC_CLIENT_SECRET`, and given to Pocket ID. Pocket ID keeps only a hash of it.
- **Issuer:** the provider stack's URL (`https://<its DOMAIN>`).
- **Order:** the client exists before the stack starts, so the app can sign users in from its first start.
- **Changes:** a new domain moves the callback. A client deleted in Pocket ID, or a secret it lost, is made again: within the next reconcile round (15 minutes) for stacks with `apply: auto`, on the next apply for others. Plans list these as `sign-on client <stack> in <provider>`, and a plan with only this change leaves the stack's containers alone.
- **What stays yours:** settings made in Pocket ID beyond the callbacks, such as allowed groups, consent and token lifetimes, are kept.

## When the provider can't be reached

Planning an integrated stack asks the provider what the client lacks. If NepFleet can't ask it (it's down, its domain doesn't resolve, the API key is refused, or the provider stack was never applied), the plan fails with the reason:

- `nepfleet plan` and `apply` report the error, and the stack isn't applied.
- An automatic apply holds the stack and sends one `apply.pending` notification. The next round tries again.

A stack is never applied with a client that might not exist.

NepFleet reaches the provider at its URL, as users do. The host NepFleet runs on must resolve the provider's domain and trust its certificate.

## Removing a stack

Stacks are removed by hand for now, and so are their clients: in Pocket ID, under OIDC clients, delete the client with the stack's name. The same goes for a stack switched from integrated to custom or none. NepFleet's clients say so in their description.

## For templates

A template that supports sign-on declares it, and uses the built-ins (see [the template format](spec/template.md#sign-on)):

```yaml
spec:
  supports: {sso: true}
  oidc:
    redirect: /auth/oidc/callback   # appended to the stack's URL
    logout: /                       # optional: where the provider may send users after signing out
    scopes: [openid, profile, email, groups]   # optional; default openid profile email
```
