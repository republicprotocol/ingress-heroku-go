# RenEx Ingress API

An official reference implementation for the RenEx Ingress, written in Go.

## Deployment

The RenEx Ingress API is configure for deployment to Heroku. It supports three environments for the Mainnet, F∅ Testnet, and Nightly Testnet.

**Mainnet**

To deploy the version of the RenEx Ingress API that uses the public Mainnet, run

```sh
git checkout develop
git push heroku-mainnet master:master
```

**F∅ Testnet**

To deploy the version of the RenEx Ingress API that uses the public F∅ Testnet, run

```sh
git checkout master
git push heroku-testnet develop:master
```

**Nightly**

To deploy the version of the RenEx Ingress API that uses the internal Nightly Testnet, run

```sh
git checkout nightly
git push heroku-nightly nightly:master
```

## Updating

The RenEx Ingress API depends on the official Go implementation of Republic Protocol. To update to the latest version, run

```sh
dep ensure -update
```