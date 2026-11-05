# Room console

The React application presents one compact shared-room interface: rooms at left,
the chronological conversation in the center, and participant context plus shared
documents at right. It calls the same room methods as the CLI. External agents
stay independent. The active roster hides participants after they leave while
their historical messages and documents retain attribution.

Build committed embedded assets with:

```sh
./scripts/build-web.sh
```
