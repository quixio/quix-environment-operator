**Quix Environment Operator: Controller Architecture**

## Overview

The Quix Environment Operator manages Kubernetes resources through a controller that reconciles `Environment` custom resources. This document outlines the controller's design principles, responsibilities, and security model.

## Core Components

### 1. Environment Resource Controller

- **Reconciliation Loop**: Watches for changes to `Environment` custom resources and triggers appropriate reconciliation logic
- **State Management**: Maintains idempotent operations with proper lifecycle handling (creation, updates, deletion)
- **Resource Ownership**: Manages resources with clear ownership tracking via labels

### 2. Resource Management

#### Namespace Orchestration
- Creates and manages dedicated namespaces for each environment
- Applies consistent naming pattern with configurable suffix
- Handles metadata propagation from Environment specs
- Manages deletion with finalizers to ensure proper cleanup

#### RBAC Configuration
- Creates namespace-scoped `RoleBindings` linking platform service account to predefined `ClusterRole`
- Separates controller permissions from platform permissions
- Ensures bindings are created with proper security validations

## Security Architecture

### Permission Boundaries
- **Controller Service Account**: Limited to minimum required cluster-level permissions
- **Platform Service Account**: Restricted to namespace-level operations through RoleBindings
- **Validation System**: Prevents privilege escalation by validating ClusterRole permissions

### Metadata Controls
- Enforces consistent labeling with prefix validation
- Protects system labels from being overridden
- Maintains ownership validation during reconciliation

## Operational Features

### Status Management
- Tracks environment lifecycle states: `InProgress`, `Ready`, `Failed`, `Deleting`
- Records resource-specific status for namespaces and role bindings
- Emits Kubernetes events for significant actions and errors

### Configuration
- Customizable namespace naming
- Configurable service account and role references
- Tunable reconciliation parameters

## Reconciliation Flow

```mermaid
flowchart TD
    Start([Start Reconcile]) --> GetEnv[Get Environment Resource]
    GetEnv --> |Not Found| End([End: No Action])
    GetEnv --> |Found| CheckDel{Is Deleting?}
    
    CheckDel --> |Yes| Deleting[Set Status to Deleting]
    Deleting --> DeleteRB[Delete RoleBinding]
    DeleteRB --> CheckNS{Namespace Exists?}
    CheckNS --> |Yes| IsNSDeleting{Is NS Deleting?}
    IsNSDeleting --> |No| DeleteNS[Delete Namespace]
    IsNSDeleting --> |Yes| WaitNS[Wait for NS Deletion]
    DeleteNS --> |Success| WaitNS
    DeleteNS --> |Not Managed| SkipWait[Skip Waiting]
    CheckNS --> |No| RemoveFinalizer[Remove Finalizer]
    WaitNS --> |Not Done| Requeue([Requeue])
    WaitNS --> |Done| RemoveFinalizer
    SkipWait --> RemoveFinalizer
    RemoveFinalizer --> End
    
    CheckDel --> |No| CheckFinalizer{Has Finalizer?}
    CheckFinalizer --> |No| AddFinalizer[Add Finalizer]
    AddFinalizer --> Requeue
    
    CheckFinalizer --> |Yes| CheckNamespace{Namespace Exists?}
    CheckNamespace --> |No| CreateNamespace[Create Namespace]
    CreateNamespace --> |Success| UpdateNSStatus[Set NS Status to Active]
    CreateNamespace --> |Error| FailStatus[Set Status to Failed]
    FailStatus --> End
    
    UpdateNSStatus --> CreateRB[Create RoleBinding]
    CreateRB --> |Success| UpdateRBStatus[Set RB Status to Active]
    CreateRB --> |Error| FailStatus
    
    UpdateRBStatus --> SetReady[Set Status to Ready]
    SetReady --> End
    
    CheckNamespace --> |Yes| ReconcileNS[Reconcile Namespace]
    ReconcileNS --> |Success| ReconcileRB[Reconcile RoleBinding]
    ReconcileNS --> |Error| FailStatus
    
    ReconcileRB --> |Success| SetReady
    ReconcileRB --> |Security Error| FailStatus
    ReconcileRB --> |Other Error| FailStatus
```

## Implementation Notes

- **Platform ClusterRole**: Defined in [platform-cluster-role.yaml](../deploy/quix-environment-operator/templates/platform-cluster-role.yaml)
- **Operator ClusterRole**: Defined in [operator-cluster-role.yaml](../deploy/quix-environment-operator/templates/operator-cluster-role.yaml)