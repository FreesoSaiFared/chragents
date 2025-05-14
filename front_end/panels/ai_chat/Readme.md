# AI Chat Panel

This panel is a Multi-Agent Framework that allows a user to connect an existing LLM with the chromium browser.

### Steps to run project

1. Setup the depot_tools to fetch the chromium dev tools using the instructions provided [here](https://www.chromium.org/developers/how-tos/get-the-code/) copied here:

## Set Up the depot tools for linux
# Checking out and building Chromium on Linux
There are instructions for other platforms linked from the
[get the code](../get_the_code.md) page.
## Instructions for Google Employees
Are you a Google employee? See
[go/building-chrome](https://goto.google.com/building-chrome) instead.
[TOC]
## System requirements
* An x86-64 machine with at least 8GB of RAM. More than 16GB is highly
    recommended. If your machine has an SSD, it is recommended to have
    \>=32GB/>=16GB of swap for machines with 8GB/16GB of RAM respectively.
* At least 100GB of free disk space. It does not have to be on the same drive;
 Allocate ~50-80GB on HDD for build.
* You must have Git and Python v3.9+ installed already (and `python3` must point
    to a Python v3.9+ binary). Depot_tools bundles an appropriate version
    of Python in `$depot_tools/python-bin`, if you don't have an appropriate
    version already on your system.
* Chromium's build infrastructure and `depot_tools` currently use Python 3.11.
  If something is broken with an older Python version, feel free to report or
  send us fixes.
* `libc++` is currently the only supported STL. `clang` is the only
  officially-supported compiler, though external community members generally
  keep things building with `gcc`. For more details, see the
  [supported toolchains doc](../toolchain_support.md).
Most development is done on Ubuntu (Chromium's build infrastructure currently
runs 22.04, Jammy Jellyfish). There are some instructions for other distros
below, but they are mostly unsupported, but installation instructions can be found in [Docker](#docker).
## Install `depot_tools`
Clone the `depot_tools` repository:
```shell
$ git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
```
Add `depot_tools` to the beginning of your `PATH` (you will probably want to put
this in your `~/.bashrc` or `~/.zshrc`). Assuming you cloned `depot_tools` to
`/path/to/depot_tools`:
```shell
$ export PATH="/path/to/depot_tools:$PATH"
```
When cloning `depot_tools` to your home directory **do not** use `~` on PATH,
otherwise `gclient runhooks` will fail to run. Rather, you should use either
`$HOME` or the absolute path:
```shell
$ export PATH="${HOME}/depot_tools:$PATH"
```
## Get the code
Create a `chromium` directory for the checkout and change to it (you can call
this whatever you like and put it wherever you like, as long as the full path
has no spaces):
```shell
$ mkdir ~/chromium && cd ~/chromium
```
Run the `fetch` tool from depot_tools to check out the code and its
dependencies.
```shell
$ fetch --nohooks chromium
```
*** note
**NixOS users:** tools like `fetch` won’t work without a Nix shell. Clone [the
tools repo](https://chromium.googlesource.com/chromium/src/tools) with `git`,
then run `nix-shell tools/nix/shell.nix`.
***
If you don't want the full repo history, you can save a lot of time by
adding the `--no-history` flag to `fetch`.
Expect the command to take 30 minutes on even a fast connection, and many
hours on slower ones.
If you've already installed the build dependencies on the machine (from another
checkout, for example), you can omit the `--nohooks` flag and `fetch`
will automatically execute `gclient runhooks` at the end.
When `fetch` completes, it will have created a hidden `.gclient` file and a
directory called `src` in the working directory. The remaining instructions
assume you have switched to the `src` directory:
```shell
$ cd src
```
### Install additional build dependencies
Once you have checked out the code, and assuming you're using Ubuntu, run
[build/install-build-deps.sh](/build/install-build-deps.sh)
```shell
$ ./build/install-build-deps.sh
```
You may need to adjust the build dependencies for other distros. There are
some [notes](#notes-for-other-distros) at the end of this document, but we make no guarantees
for their accuracy.
### Run the hooks
Once you've run `install-build-deps` at least once, you can now run the
Chromium-specific hooks, which will download additional binaries and other
things you might need:
```shell
$ gclient runhooks
```
*Optional*: You can also [install API
keys](https://www.chromium.org/developers/how-tos/api-keys) if you want your
build to talk to some Google services, but this is not necessary for most
development and testing purposes.
## Setting up the build
Chromium uses [Siso](https://pkg.go.dev/go.chromium.org/infra/build/siso#section-readme)
as its main build tool along with
a tool called [GN](https://gn.googlesource.com/gn/+/main/docs/quick_start.md)
to generate `.ninja` files. You can create any number of *build directories*
with different configurations. To create a build directory, run:
```shell
$ gn gen out/Default
```
* You only have to run this once for each new build directory, Siso will
  update the build files as needed.
* You can replace `Default` with another name, but
  it should be a subdirectory of `out`.
* For other build arguments, including release settings, see [GN build
  configuration](https://www.chromium.org/developers/gn-build-configuration).
  The default will be a debug component build matching the current host
  operating system and CPU.
* For more info on GN, run `gn help` on the command line or read the
  [quick start guide](https://gn.googlesource.com/gn/+/main/docs/quick_start.md).
### Faster builds
This section contains some things you can change to speed up your builds,
sorted so that the things that make the biggest difference are first.
#### Use Remote Execution
*** note
**Warning:** If you are a Google employee, do not follow the instructions below.
See
[go/chrome-linux-build#setup-remote-execution](https://goto.google.com/chrome-linux-build#setup-remote-execution)
instead.
***
Chromium's build can be sped up significantly by using a remote execution system
compatible with [REAPI](https://github.com/bazelbuild/remote-apis). This allows
you to benefit from remote caching and executing many build actions in parallel
on a shared cluster of workers.
Chromium's build uses a client developed by Google called
[Siso](https://pkg.go.dev/go.chromium.org/infra/build/siso#section-readme)
to remotely execute build actions.
To get started, you need access to an REAPI-compatible backend.
The following instructions assume that you received an invitation from Google
to use Chromium's RBE service and were granted access to it.
For contributors who have
[tryjob access](https://www.chromium.org/getting-involved/become-a-committer/#try-job-access)
, please ask a Googler to email accounts@chromium.org on your behalf to access
RBE backend paid by Google. Note that remote execution for external
contributors is a best-effort process. We do not guarantee when you will be
invited.
For others who have no access to Google's RBE backends, you are welcome
to use any of the
[other compatible backends](https://github.com/bazelbuild/remote-apis#servers),
in which case you will have to adapt the following instructions regarding the
authentication method, instance name, etc. to work with your backend.
If you would like to use `siso` with Google's RBE,
you'll first need to:
1. Run `siso login` and login with your authorized account.
If it is blocked in OAuth2 flow, run `gcloud auth login` instead.
Next, you'll have to specify your `rbe_instance` in your `.gclient`
configuration to use the correct one for Chromium contributors:
*** note
**Warning:** If you are a Google employee, do not follow the instructions below.
See
[go/chrome-linux-build#setup-remote-execution](https://goto.google.com/chrome-linux-build#setup-remote-execution)
instead.
***
```
solutions = [
  {
    ...,
    "custom_vars": {
      # This is the correct instance name for using Chromium's RBE service.
      # You can only use it if you were granted access to it. If you use your
      # own REAPI-compatible backend, you will need to change this accordingly
      # to its requirements.
      "rbe_instance": "projects/rbe-chromium-untrusted/instances/default_instance",
    },
  },
]
```
And run `gclient sync`. This will regenerate the config files in
`build/config/siso/backend_config/backend.star` to use the `rbe_instance`
that you just added to your `.gclient` file.
If `rbe_instance` is not owned by Google, you may need to create your
own `backend.star`. See
[build/config/siso/backend_config/README.md](../../build/config/siso/backend_config/README.md).
Then, add the following GN args to your `args.gn`:
```
use_remoteexec = true
use_siso = true
```
If `args.gn` contains `use_reclient=true`, drop it or replace it with
`use_reclient=false`.
That's it. Remember to always use `autoninja` for building Chromium as described
below, instead of directly invoking `siso` or `ninja`.
Reach out to
[build@chromium.org](https://groups.google.com/a/chromium.org/g/build)
if you have any questions about remote execution usage.
#### Include fewer debug symbols
By default GN produces a build with all of the debug assertions enabled
(`is_debug=true`) and including full debug info (`symbol_level=2`). Setting
`symbol_level=1` will produce enough information for stack traces, but not
line-by-line debugging. Setting `symbol_level=0` will include no debug
symbols at all. Either will speed up the build compared to full symbols.
#### Disable debug symbols for Blink and v8
Due to its extensive use of templates, the Blink code produces about half
of our debug symbols. If you don't ever need to debug Blink, you can set
the GN arg `blink_symbol_level=0`. Similarly, if you don't need to debug v8 you
can improve build speeds by setting the GN arg `v8_symbol_level=0`.
#### Use Icecc
[Icecc](https://github.com/icecc/icecream) is the distributed compiler with a
central scheduler to share build load. Currently, many external contributors use
it. e.g. Intel, Opera, Samsung (this is not useful if you're using Siso).
In order to use `icecc`, set the following GN args:
```
use_debug_fission=false
is_clang=false
```
See these links for more on the
[bundled_binutils limitation](https://github.com/icecc/icecream/commit/b2ce5b9cc4bd1900f55c3684214e409fa81e7a92),
the [debug fission limitation](http://gcc.gnu.org/wiki/DebugFission).
Using the system linker may also be necessary when using glibc 2.21 or newer.
See [related bug](https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=808181).
#### ccache
You can use [ccache](https://ccache.dev) to speed up local builds (again,
this is not useful if you're using Siso).
Increase your ccache hit rate by setting `CCACHE_BASEDIR` to a parent directory
that the working directories all have in common (e.g.,
`/home/yourusername/development`). Consider using
`CCACHE_SLOPPINESS=include_file_mtime` (since if you are using multiple working
directories, header times in svn sync'ed portions of your trees will be
different - see
[the ccache troubleshooting section](https://ccache.dev/manual/latest.html#_troubleshooting)
for additional information). If you use symbolic links from your home directory
to get to the local physical disk directory where you keep those working
development directories, consider putting
```
alias cd="cd -P"
```
in your `.bashrc` so that `$PWD` or `cwd` always refers to a physical, not
logical directory (and make sure `CCACHE_BASEDIR` also refers to a physical
parent).
If you tune ccache correctly, a second working directory that uses a branch
tracking trunk and is up to date with trunk and was gclient sync'ed at about the
same time should build chrome in about 1/3 the time, and the cache misses as
reported by `ccache -s` should barely increase.
This is especially useful if you use
[git-worktree](http://git-scm.com/docs/git-worktree) and keep multiple local
working directories going at once.
#### Using tmpfs
You can use tmpfs for the build output to reduce the amount of disk writes
required. I.e. mount tmpfs to the output directory where the build output goes:
As root:
```
mount -t tmpfs -o size=20G,nr_inodes=40k,mode=1777 tmpfs /path/to/out
```
*** note
**Caveat:** You need to have enough RAM + swap to back the tmpfs. For a full
debug build, you will need about 20 GB. Less for just building the chrome target
or for a release build.
***
Quick and dirty benchmark numbers on a HP Z600 (Intel core i7, 16 cores
hyperthreaded, 12 GB RAM)
* With tmpfs:
  * 12m:20s
* Without tmpfs
  * 15m:40s
### Smaller builds
The Chrome binary contains embedded symbols by default. You can reduce its size
by using the Linux `strip` command to remove this debug information. You can
also reduce binary size and turn on all optimizations by enabling official build
mode, with the GN arg `is_official_build = true`.
## Build Chromium
Build Chromium (the "chrome" target) with Siso or Ninja using the command:
```shell
$ autoninja -C out/Default chrome
```
(`autoninja` is a wrapper that automatically provides optimal values for the
arguments passed to `siso` or `ninja`.)
You can get a list of all of the other build targets from GN by running `gn ls
out/Default` from the command line. To compile one, pass the GN label to
Siso/Ninja with no preceding "//" (so, for `//chrome/test:unit_tests` use
`autoninja -C out/Default chrome/test:unit_tests`).
## Compile a single file
Siso/Ninja supports a special [syntax `^`][ninja hat syntax] to compile a single object file specifying
the source file. For example, `autoninja -C out/Default ../../base/logging.cc^`
compiles `obj/base/base/logging.o`.
[ninja hat syntax]: https://ninja-build.org/manual.html#:~:text=There%20is%20also%20a%20special%20syntax%20target%5E%20for%20specifying%20a%20target%20as%20the%20first%20output%20of%20some%20rule%20containing%20the%20source%20you%20put%20in%20the%20command%20line%2C%20if%20one%20exists.%20For%20example%2C%20if%20you%20specify%20target%20as%20foo.c%5E%20then%20foo.o%20will%20get%20built%20(assuming%20you%20have%20those%20targets%20in%20your%20build%20files)
In addition to `foo.cc^`, Siso also supports `foo.h^` syntax to compile
the corresponding `foo.o` if it exists.
## Run Chromium
Once it is built, you can simply run the browser:
```shell
$ out/Default/chrome
```
If you're using a remote machine that supports Chrome Remote Desktop, you can
add this to your .bashrc / .bash_profile.
```shell
if [[ -z "${DISPLAY}" ]]; then
  # In reality, Chrome Remote Desktop starts with 20 and increases until it
  # finds an available ID [1]. So this isn't guaranteed to always work, but
  # should work on the vast majoriy of cases.
  #
  # [1] https://source.chromium.org/chromium/chromium/src/+/main:remoting/host/linux/linux_me2me_host.py;l=112;drc=464a632e21bcec76c743930d4db8556613e21fd8
  export DISPLAY=:20
fi
```
This means if you launch Chrome from an SSH session, the UI output will be
available in Chrome Remote Desktop.
## Running test targets
Tests are split into multiple test targets based on their type and where they
exist in the directory structure. To see what target a given unit test or
browser test file corresponds to, the following command can be used:
```shell
$ gn refs out/Default --testonly=true --type=executable --all chrome/browser/ui/browser_list_unittest.cc
//chrome/test:unit_tests
```
In the example above, the target is unit_tests. The unit_tests binary can be
built by running the following command:
```shell
$ autoninja -C out/Default unit_tests
```
You can run the tests by running the unit_tests binary. You can also limit which
tests are run using the `--gtest_filter` arg, e.g.:
```shell
$ out/Default/unit_tests --gtest_filter="BrowserListUnitTest.*"
```
You can find out more about GoogleTest at its
[GitHub page](https://github.com/google/googletest).
## Update your checkout
To update an existing checkout, you can run
```shell
$ git rebase-update
$ gclient sync
```
The first command updates the primary Chromium source repository and rebases
any of your local branches on top of tip-of-tree (aka the Git branch
`origin/main`). If you don't want to use this script, you can also just use
`git pull` or other common Git commands to update the repo.
The second command syncs dependencies to the appropriate versions and re-runs
hooks as needed.
## Tips, tricks, and troubleshooting
### Linker Crashes
If, during the final link stage:
```
LINK out/Debug/chrome
```
You get an error like:
```
collect2: ld terminated with signal 6 Aborted terminate called after throwing an instance of 'std::bad_alloc'
collect2: ld terminated with signal 11 [Segmentation fault], core dumped
```
or:
```
LLVM ERROR: out of memory
```
you are probably running out of memory when linking. You *must* use a 64-bit
system to build. Try the following build settings (see [GN build
configuration](https://www.chromium.org/developers/gn-build-configuration) for
other settings):
* Build in release mode (debugging symbols require more memory):
    `is_debug = false`
* Turn off symbols: `symbol_level = 0`
* Build in component mode (this is for development only, it will be slower and
    may have broken functionality): `is_component_build = true`
* For official (ThinLTO) builds on Linux, increase the vm.max_map_count kernel
    parameter: increase the `vm.max_map_count` value from default (like 65530)
    to for example 262144. You can run the `sudo sysctl -w vm.max_map_count=262144`
    command to set it in the current session from the shell, or add the
    `vm.max_map_count=262144` to /etc/sysctl.conf to save it permanently.
### More links
* Information about [building with Clang](../clang.md).
* You may want to [use a chroot](using_a_chroot.md) to
    isolate yourself from versioning or packaging conflicts.
* Cross-compiling for ARM? See [LinuxChromiumArm](chromium_arm.md).
* Want to use Eclipse as your IDE? See
    [LinuxEclipseDev](eclipse_dev.md).
* Want to use your built version as your default browser? See
    [LinuxDevBuildAsDefaultBrowser](dev_build_as_default_browser.md).
## Next Steps
If you want to contribute to the effort toward a Chromium-based browser for
Linux, please check out the [Linux Development page](development.md) for
more information.
## Notes for other distros
### Arch Linux
Instead of running `install-build-deps.sh` to install build dependencies, run:
```shell
$ sudo pacman -S --needed python perl gcc gcc-libs bison flex gperf pkgconfig \
nss alsa-lib glib2 gtk3 nspr freetype2 cairo dbus xorg-server-xvfb \
xorg-xdpyinfo
```
For the optional packages on Arch Linux:
* `php-cgi` is provided with `pacman`
* `wdiff` is not in the main repository but `dwdiff` is. You can get `wdiff`
    in AUR/`yaourt`
### Crostini (Debian based)
First install the `file` and `lsb-release` commands for the script to run properly:
```shell
$ sudo apt-get install file lsb-release
```
Then invoke install-build-deps.sh with the `--no-arm` argument,
because the ARM toolchain doesn't exist for this configuration:
```shell
$ sudo install-build-deps.sh --no-arm
```
### Fedora
Instead of running `build/install-build-deps.sh`, run:
```shell
su -c 'yum install git python bzip2 tar pkgconfig atk-devel alsa-lib-devel \
bison binutils brlapi-devel bluez-libs-devel bzip2-devel cairo-devel \
cups-devel dbus-devel dbus-glib-devel expat-devel fontconfig-devel \
freetype-devel gcc-c++ glib2-devel glibc.i686 gperf glib2-devel \
gtk3-devel java-1.*.0-openjdk-devel libatomic libcap-devel libffi-devel \
libgcc.i686 libjpeg-devel libstdc++.i686 libX11-devel libXScrnSaver-devel \
libXtst-devel libxkbcommon-x11-devel ncurses-compat-libs nspr-devel nss-devel \
pam-devel pango-devel pciutils-devel pulseaudio-libs-devel zlib.i686 httpd \
mod_ssl php php-cli python-psutil wdiff xorg-x11-server-Xvfb'
```
The fonts needed by Blink's web tests can be obtained by following [these
instructions](https://gist.github.com/pwnall/32a3b11c2b10f6ae5c6a6de66c1e12ae).
For the optional packages:
* `php-cgi` is provided by the `php-cli` package.
* `sun-java6-fonts` is covered by the instructions linked above.
### Gentoo
You can just run `emerge www-client/chromium`.
### NixOS
To get a shell with the dev environment:
```sh
$ nix-shell tools/nix/shell.nix
```
To run a command in the dev environment:
```sh
$ NIX_SHELL_RUN='autoninja -C out/Default chrome' nix-shell tools/nix/shell.nix
```
To set up clangd with remote indexing support, run the command below, then copy
the path into your editor config:
```sh
$ NIX_SHELL_RUN='readlink /usr/bin/clangd' nix-shell tools/nix/shell.nix
```
### OpenSUSE
Use `zypper` command to install dependencies:
(openSUSE 11.1 and higher)
```shell
sudo zypper in subversion pkg-config python perl bison flex gperf \
     mozilla-nss-devel glib2-devel gtk-devel wdiff lighttpd gcc gcc-c++ \
     mozilla-nspr mozilla-nspr-devel php5-fastcgi alsa-devel libexpat-devel \
     libjpeg-devel libbz2-devel
```
For 11.0, use `libnspr4-0d` and `libnspr4-dev` instead of `mozilla-nspr` and
`mozilla-nspr-devel`, and use `php5-cgi` instead of `php5-fastcgi`.
(openSUSE 11.0)
```shell
sudo zypper in subversion pkg-config python perl \
     bison flex gperf mozilla-nss-devel glib2-devel gtk-devel \
     libnspr4-0d libnspr4-dev wdiff lighttpd gcc gcc-c++ libexpat-devel \
     php5-cgi alsa-devel gtk3-devel jpeg-devel
```
The Ubuntu package `sun-java6-fonts` contains a subset of Java of the fonts used.
Since this package requires Java as a prerequisite anyway, we can do the same
thing by just installing the equivalent openSUSE Sun Java package:
```shell
sudo zypper in java-1_6_0-sun
```
WebKit is currently hard-linked to the Microsoft fonts. To install these using `zypper`
```shell
sudo zypper in fetchmsttfonts pullin-msttf-fonts
```
To make the fonts installed above work, as the paths are hardcoded for Ubuntu,
create symlinks to the appropriate locations:
```shell
sudo mkdir -p /usr/share/fonts/truetype/msttcorefonts
sudo ln -s /usr/share/fonts/truetype/arial.ttf /usr/share/fonts/truetype/msttcorefonts/Arial.ttf
sudo ln -s /usr/share/fonts/truetype/arialbd.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/arialbi.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/ariali.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/comic.ttf /usr/share/fonts/truetype/msttcorefonts/Comic_Sans_MS.ttf
sudo ln -s /usr/share/fonts/truetype/comicbd.ttf /usr/share/fonts/truetype/msttcorefonts/Comic_Sans_MS_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/cour.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New.ttf
sudo ln -s /usr/share/fonts/truetype/courbd.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/courbi.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/couri.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/impact.ttf /usr/share/fonts/truetype/msttcorefonts/Impact.ttf
sudo ln -s /usr/share/fonts/truetype/times.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman.ttf
sudo ln -s /usr/share/fonts/truetype/timesbd.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/timesbi.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/timesi.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/verdana.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana.ttf
sudo ln -s /usr/share/fonts/truetype/verdanab.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/verdanai.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/verdanaz.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Bold_Italic.ttf
```
The Ubuntu package `sun-java6-fonts` contains a subset of Java of the fonts used.
Since this package requires Java as a prerequisite anyway, we can do the same
thing by just installing the equivalent openSUSE Sun Java package:
```shell
sudo zypper in java-1_6_0-sun
```
WebKit is currently hard-linked to the Microsoft fonts. To install these using `zypper`
```shell
sudo zypper in fetchmsttfonts pullin-msttf-fonts
```
To make the fonts installed above work, as the paths are hardcoded for Ubuntu,
create symlinks to the appropriate locations:
```shell
sudo mkdir -p /usr/share/fonts/truetype/msttcorefonts
sudo ln -s /usr/share/fonts/truetype/arial.ttf /usr/share/fonts/truetype/msttcorefonts/Arial.ttf
sudo ln -s /usr/share/fonts/truetype/arialbd.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/arialbi.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/ariali.ttf /usr/share/fonts/truetype/msttcorefonts/Arial_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/comic.ttf /usr/share/fonts/truetype/msttcorefonts/Comic_Sans_MS.ttf
sudo ln -s /usr/share/fonts/truetype/comicbd.ttf /usr/share/fonts/truetype/msttcorefonts/Comic_Sans_MS_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/cour.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New.ttf
sudo ln -s /usr/share/fonts/truetype/courbd.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/courbi.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/couri.ttf /usr/share/fonts/truetype/msttcorefonts/Courier_New_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/impact.ttf /usr/share/fonts/truetype/msttcorefonts/Impact.ttf
sudo ln -s /usr/share/fonts/truetype/times.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman.ttf
sudo ln -s /usr/share/fonts/truetype/timesbd.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/timesbi.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Bold_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/timesi.ttf /usr/share/fonts/truetype/msttcorefonts/Times_New_Roman_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/verdana.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana.ttf
sudo ln -s /usr/share/fonts/truetype/verdanab.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Bold.ttf
sudo ln -s /usr/share/fonts/truetype/verdanai.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Italic.ttf
sudo ln -s /usr/share/fonts/truetype/verdanaz.ttf /usr/share/fonts/truetype/msttcorefonts/Verdana_Bold_Italic.ttf
```
And then for the Java fonts:
```shell
sudo mkdir -p /usr/share/fonts/truetype/ttf-lucida
sudo find /usr/lib*/jvm/java-1.6.*-sun-*/jre/lib -iname '*.ttf' -print \
     -exec ln -s {} /usr/share/fonts/truetype/ttf-lucida \;
```
### Docker
#### Prerequisites
While it is not a common setup, Chromium compilation should work from within a
Docker container. If you choose to compile from within a container for whatever
reason, you will need to make sure that the following tools are available:
* `curl`
* `git`
* `lsb_release`
* `python3`
* `sudo`
* `file`
There may be additional Docker-specific issues during compilation. See
[this bug](https://crbug.com/1377520) for additional details on this.
Note: [Clone depot_tools](#install-depot_tools) first.
#### Build Steps
1. Put the following Dockerfile in `/path/to/chromium/`.
```docker
# Use an official Ubuntu base image with Docker already installed
FROM ubuntu:22.04
# Set environment variables
ENV DEBIAN_FRONTEND=noninteractive
# Install Mantatory tools (curl git python3) and optional tools (vim sudo)
RUN apt-get update && \
    apt-get install -y curl git lsb-release python3 git file vim sudo && \
    rm -rf /var/lib/apt/lists/*
# Export depot_tools path
ENV PATH="/depot_tools:${PATH}"
# Configure git for safe.directory
RUN git config --global --add safe.directory /depot_tools && \
    git config --global --add safe.directory /chromium/src
# Set the working directory to the existing Chromium source directory.
# This can be either "/chromium/src" or "/chromium".
WORKDIR /chromium/src
# Expose any necessary ports (if needed)
# EXPOSE 8080
# Create a dummy user and group to avoid permission issues
RUN groupadd -g 1001 chrom-d && \
    useradd -u 1000 -g 1001 -m chrom-d
# Create normal user with name "chrom-d". Optional and you can use root but
# not advised.
USER chrom-d
# Start Chromium Builder "chrom-d" (modify this command as needed)
# CMD ["autoninja -C out/Default chrome"]
CMD ["bash"]
```
2. Build Container
```shell
# chrom-b is just a name; You can change it but you must reflect the renaming
# in all commands below
$ docker build -t chrom-b .
```
3. Run container as root to install dependencies
```shell
$ docker run
  -it \ # Run docker interactively
  --name chrom-b \ # with name "chrom-b"
  -u root \ # with user root
  -v /path/on/machine/to/chromium:/chromium \ # With chromium folder mounted
  -v /path/on/machine/to/depot_tools:/depot_tools \ # With depot_tools mounted
  chrom-b # Run container with image name "chrom-b"
```
*** note
**Note:** When running the command as a single line in bash, please remove the
comments (after the `#`) to avoid breaking the command.
***
4. Install dependencies:
```shell
./build/install-build-deps.sh
```
5. [Run hooks](#run-the-hooks) (On docker or machine if you installed depot_tools on machine)
*** note
**Before running hooks:** Ensure that all directories within
`third_party` are added as safe directories in Git. This is required
when running in the container because the ownership of the `src/`
directory (e.g., `chrom-b`) differs from the current user
(e.g., `root`). To prevent Git **warnings** about "dubious ownership"
run the following command after installing the dependencies:
```shell
# Loop through each directory in /chromium/src/third_party and add
# them as safe directories in Git
$ for dir in /chromium/src/third_party/*; do
    if [ -d "$dir" ]; then
        git config --global --add safe.directory "$dir"
    fi
done
```
***
6. Exit container
7. Save container image with tag-id name `dpv1.0`. Run this on the machine, not in container
```shell
# Get docker running/stopped containers, copy the "chrom-b" id
$ docker container ls -a
# Save/tag running docker container with name "chrom-b" with "dpv1.0"
# You can choose any tag name you want but propagate name accordingly
# You will need to create new tags when working on different parts of
# chromium which requires installing additional dependencies
$ docker commit <ID from above step> chrom-b:dpv1.0
# Optional, just saves space by deleting unnecessary images
$ docker image rmi chrom-b:latest && docker image prune \
  && docker container prune && docker builder prune
```
#### Run container
```shell
$ docker run --rm \ # close instance upon exit
  -it \ # Run docker interactively
  --name chrom-b \ # with name "chrom-b"
  -u $(id -u):$(id -g) \ # Run container as a non-root user with same UID & GID
  -v /path/on/machine/to/chromium:/chromium \ # With chromium folder mounted
  -v /path/on/machine/to/depot_tools:/depot_tools \ # With depot_tools mounted
  chrom-b:dpv1.0 # Run container with image name "chrom-b" and tag dpv1.0
```
*** note
**Note:** When running the command as a single line in bash, please remove the
comments (after the `#`) to avoid breaking the command.
***
## End of setting up the depot tools... (for linux) 

```sh
git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
export PATH="$PATH:/path/to/depot_tools"
```
2. Follow this instructions to [set up](https://chromium.googlesource.com/devtools/devtools-frontend/+/main/docs/get_the_code.md) dev tools
## Copied here

# Get the Code: Checkout and Build Chromium DevTools front-end
In order to make changes to DevTools frontend, build, run, test, and submit changes, several workflows exist. Having [depot_tools](https://commondatastorage.googleapis.com/chrome-infra-docs/flat/depot_tools/docs/html/depot_tools_tutorial.html#_setting_up) set up is a common prerequisite.
[TOC]
## Standalone checkout
As a standalone project, Chrome DevTools frontend can be checked out and built independently from Chromium. The main advantage is not having to check out and build Chromium.
However, to run layout tests, you need to use the [chromium checkout](#Chromium-checkout) or [integrated checkout](#Integrated-checkout).
### Checking out source
To check out the source for DevTools frontend only, follow these steps:
```bash
mkdir devtools
cd devtools
fetch devtools-frontend
```
### Build
To build, follow these steps:
```bash
cd devtools-frontend
gclient sync
npm run build
```
The resulting build artifacts can be found in `out/Default/gen/front_end`.
The build tools generally assume `Default` as the target (and `out/Default` as the
build directory). You can pass `-t <name>` (or `--target=<name>`) to use a different
target. For example
```bash
npm run build -- -t Debug
```
will build in `out/Debug` instead of `out/Default`. If the directory doesn't exist,
it'll automatically create and initialize it.
You can disable type checking (via TypeScript) by using the `devtools_skip_typecheck`
argument in your GN configuration. This uses [esbuild](https://esbuild.github.io/)
instead of `tsc` to compile the TypeScript files and generally results in much
shorter build times. To switch the `Default` target to esbuild, use
```bash
gn gen out/Default --args="devtools_skip_typecheck=true"
```
or if you don't want to change the default target, use something like
```bash
gn gen out/fast-build --args="devtools_skip_typecheck=true"
```
and use `npm run build -- -t fast-build` to build this target.
### Rebuilding automatically
You can use
```bash
npm run build -- --watch
```
to have the build script watch for changes in source files and automatically trigger
rebuilds as necessary.
#### Linux system limits
The watch mode uses `inotify` by default on Linux to monitor directories for changes.
It's fairly common to encounter a system limit on the number of files you can monitor.
For example, Ubuntu Lucid's (64bit) inotify limit is set to 8192.
You can get your current inotify file watch limit by executing:
```bash
cat /proc/sys/fs/inotify/max_user_watches
```
When this limit is not enough to monitor all files inside a directory, the limit must
be increased for the watch mode to work properly. You can set a new limit temporary with:
```bash
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl -p
```
If you like to make your limit permanent, use:
```bash
echo fs.inotify.max_user_watches=524288 | sudo tee -a /etc/sysctl.conf
sudo sysctl -p
```
You may also need to pay attention to the values of `max_queued_events` and `max_user_instances`
if you encounter any errors.
### Update to latest
To update to latest tip of tree version:
```bash
git fetch origin; git checkout origin/main  # or, alternatively: git rebase-update
gclient sync
```
### Out of sync dependencies and cross-repo changes
The revisions of git dependencies must always be in sync between the entry in DEPS and the git submodule. PRESUBMIT will
reject CLs that try to submit changes to one but not the other.
It can happen that dependencies go out of sync for three main reasons:
1. The developer attempted a manual roll by only updating the DEPS file (which was the process before migrating to git
   submodules, see [below](#Managing-dependencies)),
1. after switching branches or checking out new commit the developer didn't run `gclient sync`, or
1. they are working across repositories including changes in both.
In the first case, follow the [manual roll process](#Managing-dependencies). In the second case,
running `gclient sync` is necessary. If the changes to the submodule versions were already added to any commits (this
happens when commits were created using `git add -A`, for example), it's necessary to unstage them (for example using
`git checkout -p origin/main`). The latter also applies in the third case: Create a CL excluding the dependency changes
and a separate CL with a proper roll.
### Run in a pre-built Chromium
You can run a [build](#Build) of DevTools frontend in a pre-built Chrome or Chromium in order to avoid the expensive build. Options:
- Use the downloaded Chrome for Testing binary in `third_party/chrome`.
- Use the latest Chrome Canary. This includes any DevTools features that are only available in regular Chrome builds (`is_official_build` + `is_chrome_branded`), such as GenAI-related features.
#### Using `npm start` script (recommended)
With Chromium 136, we added (back) a `start` script that can be used to easily launch DevTools with pre-built [Chrome
for Testing](https://developer.chrome.com/blog/chrome-for-testing) or [Chrome Canary](https://www.google.com/chrome/canary/).
It'll also take care of automatically enabling/disabling experimental features that are actively being worked on. Use
```bash
npm start
```
to build DevTools front-end in `out/Default` (you can change this to `out/foo` by passing `--target=foo` if needed),
and open Chrome for Testing (in `third_party/chrome`) with the custom DevTools front-end. This will also monitor the
source files for changes while Chrome is running and automatically trigger a rebuild whenever source files change.
By default, `npm start` will automatically open DevTools for every new tab, you can use
```bash
npm start -- --no-open
```
to disable this behavior. You can also use
```bash
npm start -- --browser=canary
```
to run in Chrome Canary instead of Chrome for Testing; this requires you to install Chrome Canary manually first
(Googlers can install `google-chrome-canary` on gLinux). And finally use
```bash
npm start -- http://www.example.com
```
to automatically open `http://www.example.com` in the newly spawned Chrome tab. Use
```bash
npm start -- --verbose
```
to enable verbose logging, which among other things, also prints all output from Chrome to the terminal, which is
otherwise suppressed.
##### Controlling the feature set
By default `npm start` will enable a bunch of experimental features (related to DevTools) that are considered ready for teamfood.
To also enable experimental features that aren't yet considered sufficiently stable to enable them by default for the team, run:
```bash
# Long version
npm start -- --unstable-features
# Short version
npm start -- -u
```
Just like with Chrome itself, you can also control the set of enabled and disabled features using
```bash
npm start -- --enable-features=DevToolsAutomaticFileSystems
npm start -- --disable-features=DevToolsWellKnown --enable-features=DevToolsFreestyler:multimodal/true
```
which you can use to override the default feature set.
##### Remote debugging
The `npm start` command also supports launching Chrome for remote debugging via
```bash
npm start -- --remote-debugging-port=9222
```
or
```bash
npm start -- --browser=canary --remote-debugging-port=9222 --user-data-dir=\`mktemp -d`
```
Note that you have to also pass the `--user-data-dir` and point it to a non-standard profile directory (a freshly created
temporary directory in this example) for security reason when using any Chrome version except for Chrome for Testing.
[This article](https://developer.chrome.com/blog/remote-debugging-port) explains the reasons behind it.
#### Running from file system
This works with Chromium 79 or later.
**(Requires `brew install coreutils` on Mac.)**
To run on **Mac**:
```bash
<path-to-devtools-frontend>./third_party/chrome/chrome-mac/Google\ Chrome\ for\ Testing.app/Contents/MacOS/Google\ Chrome\ for\ Testing --disable-infobars --disable-features=MediaRouter --custom-devtools-frontend=file://$(realpath out/Default/gen/front_end) --use-mock-keychain
```
To run on **Linux**:
```bash
<path-to-devtools-frontend>./third_party/chrome/chrome-linux/chrome --disable-infobars --custom-devtools-frontend=file://$(realpath out/Default/gen/front_end)
```
To run on **Windows**:
```bash
<path-to-devtools-frontend>\third_party\chrome\chrome-win\chrome.exe --disable-infobars --custom-devtools-frontend="<path-to-devtools-frontend>\out\Default\gen\front_end"
```
Note that `$(realpath out/Default/gen/front_end)` expands to the absolute path to build artifacts for DevTools frontend.
Open DevTools via F12 or Ctrl+Shift+J on Windows/Linux or Cmd+Option+I on Mac.
If you get errors along the line of `Uncaught TypeError: Cannot read property 'setInspectedTabId'` you probably specified an incorrect path - the path has to be absolute. On Mac and Linux, the file url will start with **three** slashes: `file:///Users/...`.
**Tip**: You can inspect DevTools with DevTools by undocking DevTools and then opening a second instance of DevTools (see keyboard shortcut above).
**Tip**: On Windows it is possible the browser will re-attach to an existing session without applying command arguments. Make sure that there are no active Chrome sessions running.
#### Running from remote URL
This works with Chromium 85 or later.
Serve the content of `out/Default/gen/front_end` on a web server, e.g. via `python -m http.server`.
Then point to that web server when starting Chromium, for example:
```bash
<path-to-devtools-frontend>/third_party/chrome/chrome-<platform>/chrome --disable-infobars --custom-devtools-frontend=http://localhost:8000/
```
Open DevTools via F12 or Ctrl+Shift+J on Windows/Linux or Cmd+Option+I on Mac.
#### Running in hosted mode
Serve the content of `out/Default/gen/front_end` on a web server, e.g. via `python3 -m http.server 8000`.
Then start Chrome, allowing for accesses from the web server:
```bash
<path-to-devtools-frontend>/third_party/chrome/chrome-<platform>/chrome --disable-infobars --remote-debugging-port=9222 --remote-allow-origins=http://localhost:8000 about:blank
```
Get the list of pages together with their DevTools frontend URLs:
```bash
$ curl http://localhost:9222/json -s | grep '\(url\|devtoolsFrontend\)'
   "devtoolsFrontendUrl": "/devtools/inspector.html?ws=localhost:9222/devtools/page/BADADD4E55BADADD4E55BADADD4E5511",
   "url": "about:blank",
```
In a regular Chrome tab, go to the URL `http://localhost:8000/inspector.html?ws=<web-socket-url>`, where `<web-socket-url>` should be replaced by
your desired DevTools web socket URL (from `devtoolsFrontendUrl`). For example, for
`"devtoolsFrontendUrl": "/devtools/inspector.html?ws=localhost:9222/devtools/page/BADADD4E55BADADD4E55BADADD4E5511"`,
you could run the hosted DevTools with the following command:
```
$ google-chrome http://localhost:8000/inspector.html?ws=localhost:9222/devtools/page/BADADD4E55BADADD4E55BADADD4E5511
```
## Integrated checkout
**This solution is experimental, please report any trouble that you run into!**
The integrated workflow offers the best of both worlds, and allows for working on both Chromium and DevTools frontend
side-by-side. This is strongly recommended for folks working primarily on DevTools.
This workflow will ensure that your local setup is equivalent to how Chromium infrastructure tests your change.
A full [Chromium checkout](#Chromium-checkout) is a pre-requisite for the following steps.
### Untrack the existing devtools-frontend submodule
First, you need to untrack the existing devtools-frontend submodule in the chromium checkout. This ensures that devtools
isn't dragged along whenever you update your chromium dependencies.
In `chromium/src`, run `gclient sync` to make sure you have installed all required submodules.
```bash
gclient sync
```
Then, disable `gclient sync` for DevTools frontend inside of Chromium by editing `.gclient` config. From
`chromium/src/`, run
```bash
vim "$(gclient root)/.gclient"
```
In the `custom_deps` section, insert this line:
```python
"src/third_party/devtools-frontend/src": None,
```
Following this step, there are two approaches to manage your standalone checkout
### Single gclient project
**Note: it's not possible anymore to manage the two projects in separate gclient projects.**
For the integrated checkout, create a single gclient project that automatically gclient sync's all dependencies for both
repositories. After checking out chromium, modify the .gclient file for `chromium/src` to add the DevTools project:
```python
solutions = [
  {
    # Chromium src project
    "name": "src",
    "url": "https://chromium.googlesource.com/chromium/src.git",
    "custom_deps": {
      "src/third_party/devtools-frontend/src": None,
    },
  },
  {
    # devtools-frontend project
    "name": "devtools-frontend",
    "managed": False,
    "url": "https://chromium.googlesource.com/devtools/devtools-frontend.git",
  }
]
```
Do not run `gclient sync` now, first create the symlink. In the same directory as the .gclient file, run:
```bash
ln -s src/third_party/devtools-frontend/src devtools-frontend
```
If you did run `gclient sync` first, remove the devtools-frontend directory and start over.
Run `gclient sync` after creating the link to fetch the dependencies for the standalone checkout.
## Chromium checkout
DevTools frontend can also be developed as part of the full Chromium checkout.
This workflow can be used to make small patches to DevTools as a Chromium engineer.
However, it is different to our infrastructure setup and how to execute general maintenance work, and therefore discouraged.
### Checking out source
Follow [instructions](https://www.chromium.org/developers/how-tos/get-the-code) to check out Chromium. DevTools frontend can be found under `third_party/devtools-frontend/src/`.
### Build
Refer to [instructions](https://www.chromium.org/developers/how-tos/get-the-code) to build Chromium.
To only build DevTools frontend, use `devtools_frontend_resources` as build target.
The resulting build artifacts for DevTools frontend can be found in `out/Default/gen/third_party/devtools-frontend/src/front_end`.
### Run
Run Chrome with bundled DevTools frontend:
```bash
out/Default/chrome
```
### End Instructions 

```sh
# fetching code
mkdir devtools
cd devtools
fetch devtools-frontend

# Build steps
cd devtools-frontend
gclient sync
npm run build
```

3. Update the code to this fork implementation
```sh
git remote add upstream git@github.com:tysonthomas9/browser-operator-devtools-frontend.git
git checkout upstream/main
```

4. Use this to run the [code](https://github.com/tysonthomas9/browser-operator-devtools-frontend/blob/main/front_end/panels/ai_chat/Readme.md)
```sh
npm run build
python -m http.server
```

5. Run Chrome or Chromium Browser instance
```sh
<path-to-devtools-frontend>/third_party/chrome/chrome-<platform>/chrome --disable-infobars --custom-devtools-frontend=http://localhost:8000/

# MacOS Example
/Applications/Google\ Chrome\ Canary.app/Contents/MacOS/Google\ Chrome\ Canary --custom-devtools-frontend=http://localhost:8000/
```

### Setup and Development

1. Build the project and use watch mode
```sh
npm run build -- --watch
```

2. Serve the content of out/Default/gen/front_end on a web server, e.g. via python -m http.server.

```sh
cd out/Default/gen/front_end

python3 -m http.server
```

3. Use the AI Chat panel.

```sh
<path-to-devtools-frontend>/third_party/chrome/chrome-<platform>/chrome --disable-infobars --custom-devtools-frontend=http://localhost:8000/
```


### Agent Architecture

The AI Chat Panel uses the multi-agent framework with the following components:

1. **State Management**: Tracks conversation history, user context, and DevTools state
2. **Tools**: Provides capabilities for DOM inspection, network analysis, and code execution
3. **Workflow**: Defines the agent's reasoning process and decision-making flow

## Reference
https://chromium.googlesource.com/devtools/devtools-frontend/+/main/docs/get_the_code.md#Standalone-checkout-Checking-out-source
