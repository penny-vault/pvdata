# Data Type Selection at Subscription Creation

## Context

Datasets can provide multiple data types (e.g., Zacks provides rating, metric, estimate, consensus, index). Currently all types are automatically included when subscribing. Users should be able to choose which types they care about to avoid creating unnecessary tables and storing unwanted data.

## Design

### User Flow

During `pvdata subscribe <provider>`, after the user selects a dataset, if that dataset declares more than one data type, a multi-select prompt appears listing all available types (all pre-checked by default). The user deselects any they don't want. Datasets with a single data type skip this prompt.

### Changes

**`cmd/subscribe.go`** -- Add a `huh.MultiSelect` form group after dataset selection. Each option shows the data type's ViewName (human-friendly) with the key as the value. Pass the selected keys to `NewSubscription`.

**`provider/subscription.go`** -- Add a `selectedTypes []string` parameter to `NewSubscription`. Filter `Dataset.DataTypes` to only include types whose key is in `selectedTypes`. `ComputeTableNames` then only generates tables for the filtered set.

**`library/database.go`** -- Guard each observation routing block in `SaveObservations`: if `subscription.DataTablesMap[key]` returns `""`, skip the save. This handles providers that emit all observation types even when the subscription only selected a subset.

### What Does NOT Change

- `Subscription.Save()` -- already creates tables based on whatever `DataTypes` is set
- `ComputeTableNames()` -- already maps from `DataTypes` to table names
- Preferred views -- already works off the subscription's `DataTypes`
- Provider Fetch functions -- still emit all types; routing handles the filtering
