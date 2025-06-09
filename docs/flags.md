# Using Flags

When creating a new instance, you can now pass in flags to be used when launching the new instance.

To use this feature, press `n` to launch a new instance. In the input that appears, type the name of the new instance, followed by `--`, and then any flags you want to pass in.

For example:

```
my-new-instance -- --version
```

This will create a new instance named `my-new-instance` and pass in the `--version` flag.

Another example:

```
my-other-instance -- --model claude-3-opus-20240229
```

This will create a new instance named `my-other-instance` and pass in the `--model claude-3-opus-20240229` flag.

The flags will be passed to the program that is configured to be run in the new instance. 