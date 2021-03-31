# GoBAT
Golang Background Traffic Generator Tooling for a large K8s Cluster.

## Purpose and Philosophy

* Load all network paths and involved HW/SW functions in the system to significant level
* Do not exceed 20-30% load anywhere to avoid overload in the event of any planned failure or resilience TCs
* Cover all relevant supported networking options/functions in the traffic design
* Different traffic types (as basic as possible to still cover K8s functionality): e.g. UDP, HTTP(S)
* Generate and terminate traffic by workload pods inside the K8s cluster (scales with cluster size)
* Simulate North-South traffic by routing packets through the DC-Edge or GW and back to the cluster (example: traffic from K8s secondary to primary network)
* Random pairing of client with server pods/services to statistically cover all nodes, paths and functions
* BAT traffic needs to survive or recover from source and destination pod restarts
* Record per stream stats (sent/dropped/RTT) every minute to capture traffic disturbances
* Packet/request frequency >100/s per stream for detecting 10 ms packet drops
* Metadata of affected streams often allows isolating root cause
* Use aggregate traffic stats for monitoring the cluster health

## Architecture

![GoBAT Logo](https://github.com/Nordix/GoBAT/blob/master/docs/images/gobat.png)

### BAT Pairing script and ConfigMap

The script queries K8s for BAT services, pods and interfaces and generates pairing ConfigMap (the script implementation is out of scope of this project). An example pairing ConfigMap is present [here](https://github.com/Nordix/GoBAT/blob/master/deployments/configMap.yaml). 

The traffic profile can also be configured using net-bat-profile Config Map. Here is an [example](https://github.com/Nordix/GoBAT/blob/master/deployments/net-bat-profile.yaml) to configure udp and http streams.

### Network BAT container

* Implemented in Go using Go-routines for concurrency.
* Currently implements udp client and server, extensible to plug other protocol clients/servers.
* TGC watches BAT ConfigMap changes. Processes pairing file and creates a TGen instance per originating stream
* TGen instance generates traffic and uses Prometheus Go client to report per stream metrics
* One TApp instance per interface to answer all incoming requests

### Prometheus PM server scraping all BAT pods for stream metrics

* Compute and store stream interval metrics in Time Series DB (TSDB)
* Compute and store summary stats in TSDB
* Grafana Dashboard to visualize summary stats
* Postprocessing scripts and trouble-shooting tools querying PM TSDB

The deployment and configuration of Prometheus and Grafana is out of scope of this project

## Network BAT Metrics in Prometheus

### Metrics per BAT stream:
* Duration
* Packets/Requests send_failed/sent/received/dropped (drop means timeout in Tgen)
* RTT quantiles: 50%, 90%, 95%, 99%

### Interval stats (a minute interval configured at PM)
* 	Packets sent/received/dropped
* 	Interval PPM drop rate

### Metrics per BAT server process
* Number of ongoing client connections

## Acknowledgements

Thanks big time to [Jan Scheurich ](https://github.com/JanScheurich) for his invaluable design inputs and code reviews.