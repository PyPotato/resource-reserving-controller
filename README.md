# resource-reserving-controller

## Annotation 重新设计

```json
[
  // 以 ownerreference 为单位标识保留的资源
  {
    "owner_kind": "...",
    "owner_uid": "...",
    "reserved_resources": [
      {
        "resource_type": "cpu",
        "reserved_quanty": "1000m"
      },
      {
        "resource_type": "memory",
        "reserved_quanty": "128Mi"
      },
      ...
    ]
  },
  {
    "owner_kind": "...",
    "owner_uid": "...",
    "reserved_resources": [
      {
        "resource_type": "cpu",
        "reserved_quanty": "1000m"
      },
      {
        "resource_type": "memory",
        "reserved_quanty": "128Mi"
      },
      ...
    ]
  },
  ...
]
```

以 ownerreference 为单位标识保留的资源，同一个负载x下的 pod 要保留的资源都集成在同一个数组项中（要注意销毁时怎么怎么办——销毁又分为减小 replica 和删除 ownerreference）