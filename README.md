# iris

- [ ] Keep track of containers
  - [x] creation
  - [x] deletion
  - [x] renames (on same node)?
  - [x] ~~moves (to another node)?~~ moving containers across hosts results in
    appropriate container creation and deletion events so we don't need to
    explicitly handle this
  - [ ] etc?
- [ ] Discover how many containers are currently available
- [ ] Give GPUs on request
- [ ] Show how many GPUs are currently available via HTTP API
