# iris

## Deploy

```
git tag v[0-9]+.[0-9]+.[0-9]+
```

```
git push origin [tag_name]
```

Release workflow will run and create release binary.

## TODO

- [x] GPU state discovery
- [x] Give vacant GPU on request from JupyterHub via HTTP API

### Option

- [ ] Keep track of containers
  - [x] creation
  - [x] deletion
  - [x] renames (on same node)?
  - [x] ~~moves (to another node)?~~ moving containers across hosts results in
    appropriate container creation and deletion events so we don't need to
    explicitly handle this
  - [ ] etc?
- [x] Discover how many containers are currently available
- [ ] Show how many GPUs are currently available via HTTP API
