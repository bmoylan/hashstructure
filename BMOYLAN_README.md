# Fork of github.com/mitchellh/hashstructure

We've made some performance improvements based on benchmarking with internal test data.

According to profiled benchmarks, the most expensive part of this package's hashing is due to reflection.
The second largest source of time (and particularly memory allocations) is in converting an item to a []byte for consumption by the hash's Write method.
We've tried to work around this API with more direct byte manipulation and the unsafe package to speed things up.

Below are the major changes introduced in this fork:
* We no longer use the binary encoding package, as it starts with an unnecessary 8-byte allocation on every write.
* Directly convert numbers to their []byte equivalents and write to the hash.
* Strings and numbers are no longer copied before hashing
* hashUpdateOrdered() writes checksums directly to hash without unnecessary conversion.

`TestUpstreamCompatibility` in `upstream_test.go` tests that the two packages produce identical hashes of the same item.
