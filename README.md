Main executable for the Binary Registry Manager project. Uses some of the other libraries (projects) in the same workspace. Serves the system.

## STORAGE SUBSYSTEM
- Theoritical artifact size limit is ~9 exabytes.
    - Is limitied by underlying file-system: 16EiB for NTFS, 16TiB for Ext4.
- Supported storage implementations:
    - SimpleFileStorage
    - ConcurrentArtifactStorage
    - HashComputingArtifactStorage
    
## EVENTING SUBSYSTEM
    - events of storage operations
    - events of registry operations
    - events of api operations

## INDEXING and QUERY SUBSYSTEM
Indexing subsystem is planned to use the eventing subsystem, to implement the required changes on indexing when a change occurs.
Query subsystem is planned to be executed on the indexes.

## CONFIGURATION SUBSYSTEM
Hierarchical configuration management, import & export.

## SCHEDULER SUBSYSTEM
Scheduled application and scripts, which run timely or on certain events.

## SERVING SUBSYSTEM
user managment, authentication & authorization, grouping & multi-tenancy, APIs, UI.


