package e2e

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	pgsqlapis "github.com/operator-backing-service-samples/postgresql-operator/pkg/apis"
	pgv1alpha1 "github.com/operator-backing-service-samples/postgresql-operator/pkg/apis/postgresql/v1alpha1"
	olmv1alpha1 "github.com/operator-framework/operator-lifecycle-manager/pkg/api/apis/operators/v1alpha1"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/operator-framework/operator-sdk/pkg/test/e2eutil"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"

	"github.com/redhat-developer/service-binding-operator/pkg/apis"
	"github.com/redhat-developer/service-binding-operator/pkg/apis/apps/v1alpha1"
	sbrcontroller "github.com/redhat-developer/service-binding-operator/pkg/controller/servicebindingrequest"
	"github.com/redhat-developer/service-binding-operator/test/mocks"
)

type Step string

const (
	DBStep  Step = "create-db"
	AppStep Step = "create-app"
	SBRStep Step = "create-sbr"
	CSVStep Step = "create-csv"
)

var (
	retryInterval  = time.Second * 5
	timeout        = time.Second * 120
	cleanupTimeout = time.Second * 5
)

// TestAddSchemesToFramework starting point of the test, it declare the CRDs that will be using
// during end-to-end tests.
func TestAddSchemesToFramework(t *testing.T) {
	logf.SetLogger(logf.ZapLogger(true))

	t.Log("Adding ServiceBindingRequestList scheme to cluster...")
	sbrlist := v1alpha1.ServiceBindingRequestList{}
	require.NoError(t, framework.AddToFrameworkScheme(apis.AddToScheme, &sbrlist))

	t.Log("Adding ClusterServiceVersionList scheme to cluster...")
	csvList := olmv1alpha1.ClusterServiceVersionList{}
	require.NoError(t, framework.AddToFrameworkScheme(olmv1alpha1.AddToScheme, &csvList))

	t.Log("Adding DatabaseList scheme to cluster...")
	dbList := pgv1alpha1.DatabaseList{}
	require.NoError(t, framework.AddToFrameworkScheme(pgsqlapis.AddToScheme, &dbList))

	t.Run("end-to-end", func(t *testing.T) {
		t.Run("scenario-db-app-sbr", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{DBStep, AppStep, SBRStep})
		})
		t.Run("scenario-app-db-sbr", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{AppStep, DBStep, SBRStep})
		})
		t.Run("scenario-db-sbr-app", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{DBStep, SBRStep, AppStep})
		})
		t.Run("scenario-app-sbr-db", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{AppStep, SBRStep, DBStep})
		})
		t.Run("scenario-sbr-db-app", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{SBRStep, DBStep, AppStep})
		})
		t.Run("scenario-sbr-app-db", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{SBRStep, AppStep, DBStep})
		})
		t.Run("scenario-csv-db-app-sbr", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{DBStep, AppStep, SBRStep})
		})
		t.Run("scenario-csv-app-db-sbr", func(t *testing.T) {
			ServiceBindingRequest(t, []Step{CSVStep, AppStep, DBStep, SBRStep})
		})
	})
}

// cleanupOptions using global variables to create the object.
func cleanupOptions(ctx *framework.TestCtx) *framework.CleanupOptions {
	return &framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: time.Duration(time.Second * retryInterval),
	}
}

// bootstrapNamespace execute scaffolding to have a new cluster initialized, and acquire a test
// namespace, the namespace name is returned and framework global variables are returned.
func bootstrapNamespace(t *testing.T, ctx *framework.TestCtx) (string, *framework.Framework) {
	t.Log("Initializing cluster resources...")
	err := ctx.InitializeClusterResources(cleanupOptions(ctx))
	if err != nil {
		t.Logf("Cluster resources initialization error: '%s'", err)
		require.True(t, errors.IsAlreadyExists(err), "failed to setup cluster resources")
	}

	// namespace name is informed on command-line or defined dinamically
	ns, err := ctx.GetNamespace()
	require.NoError(t, err)
	t.Logf("Using namespace '%s' for testing...", ns)

	f := framework.Global
	return ns, f
}

// ServiceBindingRequest bootstrap method to initialize cluster resources and setup a testing
// namespace, after bootstrap operator related tests method is called out.
func ServiceBindingRequest(t *testing.T, steps []Step) {
	t.Log("Creating a new test context...")
	ctx := framework.NewTestCtx(t)
	defer ctx.Cleanup()

	ns, f := bootstrapNamespace(t, ctx)

	// executing testing steps on operator
	serviceBindingRequestTest(t, ctx, f, ns, steps)
}

// assertDeploymentEnvFrom execute the inspection of a deployment type, making sure the containers
// are set, and are having "envFrom" directive.
func assertDeploymentEnvFrom(
	ctx context.Context,
	f *framework.Framework,
	namespacedName types.NamespacedName,
	secretRefName string,
) (*appsv1.Deployment, error) {
	d := &appsv1.Deployment{}
	if err := f.Client.Get(ctx, namespacedName, d); err != nil {
		return nil, err
	}

	containers := d.Spec.Template.Spec.Containers

	if len(containers) != 1 {
		return nil, fmt.Errorf("can't find a container in deployment-spec")
	}
	if len(containers[0].EnvFrom) != 1 {
		return nil, fmt.Errorf("can't find envFrom in first container")
	}
	if secretRefName != containers[0].EnvFrom[0].SecretRef.Name {
		return nil, fmt.Errorf("secret-ref attribute named '%s' not found", secretRefName)
	}

	return d, nil
}

// assertSBRStatus will determine if SBR is on "success" state
func assertSBRStatus(
	ctx context.Context,
	f *framework.Framework,
	namespacedName types.NamespacedName,
) error {
	sbr := &v1alpha1.ServiceBindingRequest{}
	if err := f.Client.Get(ctx, namespacedName, sbr); err != nil {
		return err
	}

	success := sbrcontroller.BindingSuccess
	if sbr.Status.BindingStatus != success {
		return fmt.Errorf("SBR '%#v' is not on '%s' status", namespacedName, success)
	}
	return nil
}

// assertSBRSecret execute the inspection in a secret created by the operator.
func assertSBRSecret(
	ctx context.Context,
	f *framework.Framework,
	namespacedName types.NamespacedName,
) (*corev1.Secret, error) {
	sbrSecret := &corev1.Secret{}
	if err := f.Client.Get(ctx, namespacedName, sbrSecret); err != nil {
		return nil, err
	}

	if _, contains := sbrSecret.Data["DATABASE_SECRET_USER"]; !contains {
		return nil, fmt.Errorf("can't find DATABASE_SECRET_USER in data")
	}
	actualUser := sbrSecret.Data["DATABASE_SECRET_USER"]
	expectedUser := []byte("user")
	if !bytes.Equal(expectedUser, actualUser) {
		return nil, fmt.Errorf("key DATABASE_SECRET_USER (%s) is different than expected (%s)",
			actualUser, expectedUser)
	}

	if _, contains := sbrSecret.Data["DATABASE_SECRET_PASSWORD"]; !contains {
		return nil, fmt.Errorf("can't find DATABASE_SECRET_PASSWORD in data")
	}
	actualPassword := sbrSecret.Data["DATABASE_SECRET_PASSWORD"]
	expectedPassword := []byte("password")
	if !bytes.Equal(expectedPassword, actualPassword) {
		return nil, fmt.Errorf("key DATABASE_SECRET_PASSWORD (%s) is different than expected (%s)",
			actualPassword, expectedPassword)
	}

	return sbrSecret, nil
}

// updateSBRSecret by exchanging all of its keys to "bogus" string.
func updateSBRSecret(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	namespacedName types.NamespacedName,
) {
	sbrSecret := &corev1.Secret{}
	require.NoError(t, f.Client.Get(ctx, namespacedName, sbrSecret))

	// intentionally bumping the object generation, so the operator will reconcile;
	generation := sbrSecret.GetGeneration()
	generation++
	sbrSecret.SetGeneration(generation)

	for k, v := range sbrSecret.Data {
		t.Logf("Replacing secret '%s=%s' with '%s=bogus'", k, string(v), k)
		sbrSecret.Data[k] = []byte("bogus")
	}

	require.NoError(t, f.Client.Update(ctx, sbrSecret))
}

// retry the informed method a few times, with sleep between attempts.
func retry(attempts int, sleep time.Duration, fn func() error) error {
	var err error
	for i := attempts; i > 0; i-- {
		err = fn()
		if err == nil {
			break
		}
		time.Sleep(sleep)
	}
	return err
}

// CreateDB implements end-to-end step for the creation of a Database CR along with the dependend
// Secret serving as a Backing Service to be bound to the application.
func CreateDB(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	cleanupOpts *framework.CleanupOptions,
	namespacedName types.NamespacedName,
	secretName string,
) *pgv1alpha1.Database {
	t.Logf("Creating Database mock object '%#v'...", namespacedName)
	ns := namespacedName.Namespace
	resourceRef := namespacedName.Name

	db := mocks.DatabaseCRMock(ns, resourceRef)
	require.NoError(t, f.Client.Create(ctx, db, cleanupOpts))

	t.Logf("Updating Database '%#v' status, adding 'DBCredentials'", namespacedName)
	require.NoError(t, f.Client.Get(ctx, namespacedName, db))
	db.Status.DBCredentials = secretName
	require.NoError(t, f.Client.Status().Update(ctx, db))

	t.Log("Creating Database credentials secret mock object...")
	dbSecret := mocks.SecretMock(ns, secretName)
	require.NoError(t, f.Client.Create(ctx, dbSecret, cleanupOpts))

	return db
}

// CreateApp implements end-to-end step for the creation of a Deployment serving as the Application
// to which the Backing Service is bound.
func CreateApp(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	cleanupOpts *framework.CleanupOptions,
	namespacedName types.NamespacedName,
	matchLabels map[string]string,
) appsv1.Deployment {
	t.Logf("Creating Deployment mock object '%#v'...", namespacedName)
	ns := namespacedName.Namespace
	appName := namespacedName.Name

	d := mocks.DeploymentMock(ns, appName, matchLabels)
	require.NoError(t, f.Client.Create(ctx, &d, cleanupOpts))

	// waiting for application deployment to reach one replica
	t.Log("Waiting for application deployment reach one replica...")
	require.NoError(
		t,
		e2eutil.WaitForDeployment(t, f.KubeClient, ns, appName, 1, retryInterval, timeout),
	)

	// retrieveing deployment, to inspect its contents
	t.Logf("Reading application deployment '%s'", appName)
	require.NoError(t, f.Client.Get(ctx, namespacedName, &d))

	return d
}

// CreateSBR implements end-to-end step for creating a Service Binding Request to bind the Backing
// Service and the Application.
func CreateSBR(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	cleanupOpts *framework.CleanupOptions,
	namespacedName types.NamespacedName,
	resourceRef string,
	matchLabels map[string]string,
) *v1alpha1.ServiceBindingRequest {
	t.Logf("Creating ServiceBindingRequest mock object '%#v'...", namespacedName)
	ns := namespacedName.Namespace
	name := namespacedName.Name
	sbr := mocks.ServiceBindingRequestMock(ns, name, resourceRef, "", matchLabels, false)
	// FIXME: why do we delete in so many places? should this be removed?
	// making sure object does not exist before testing
	_ = f.Client.Delete(ctx, sbr)
	require.NoError(t, f.Client.Create(ctx, sbr, cleanupOpts))
	return sbr
}

// CreateCSV created mocked cluster service version object.
func CreateCSV(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	cleanupOpts *framework.CleanupOptions,
	namespacedName types.NamespacedName,
) {
	t.Logf("Creating ClusterServiceVersion mock object: '%#v'...", namespacedName)
	csv := mocks.ClusterServiceVersionMock(namespacedName.Namespace, namespacedName.Name)
	require.NoError(t, f.Client.Create(ctx, &csv, cleanupOpts))
}

// inspectDeployment assert deployment resource in a retry loop.
func inspectDeployment(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	namespacedName types.NamespacedName,
	sbrName string,
) {
	err := retry(10, 5*time.Second, func() error {
		t.Logf("Inspecting deployment '%s'", namespacedName)
		_, err := assertDeploymentEnvFrom(ctx, f, namespacedName, sbrName)
		if err != nil {
			t.Logf("Error on inspecting deployment: '%#v'", err)
		}
		return err
	})
	t.Logf("Deployment: Result after attempts, error: '%#v'", err)
	require.NoError(t, err)
}

// inspectSBRStatus retry assert SBR status to make sure it's in "success" state.
func inspectSBRStatus(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	namespacedName types.NamespacedName,
) {
	err := retry(10, 5*time.Second, func() error {
		t.Logf("Inspecting SBR: '%s'", namespacedName)
		err := assertSBRStatus(ctx, f, namespacedName)
		if err != nil {
			t.Logf("Error on inspecting SBR: '%#v'", err)
		}
		return err
	})
	t.Logf("SBR-Status: Result after attempts, error: '%#v'", err)
	require.NoError(t, err)
}

// inspectSBRSecret retry assert on intermediary secret, making sure it contains expected content.
func inspectSBRSecret(
	ctx context.Context,
	t *testing.T,
	f *framework.Framework,
	namespacedName types.NamespacedName,
) {
	err := retry(10, 5*time.Second, func() error {
		t.Log("Inspecting SBR generated secret...")
		_, err := assertSBRSecret(ctx, f, namespacedName)
		if err != nil {
			t.Logf("SBR generated secret inspection error: '%#v'", err)
		}
		return err
	})
	t.Logf("Intermediary-Secret: Result after attempts, error: '%#v'", err)
	require.NoError(t, err)
}

// serviceBindingRequestTest executes the actual end-to-end testing, simulating the components and
// expecting for changes caused by the operator.
func serviceBindingRequestTest(
	t *testing.T,
	ctx *framework.TestCtx,
	f *framework.Framework,
	ns string,
	steps []Step,
) {
	// making sure resource names employed during test are unique
	randomSuffix := rand.Int()
	csvName := fmt.Sprintf("cluster-service-version-%d", randomSuffix)
	sbrName := fmt.Sprintf("e2e-service-binding-request-%d", randomSuffix)
	resourceRef := fmt.Sprintf("e2e-db-testing-%d", randomSuffix)
	secretName := fmt.Sprintf("e2e-db-credentials-%d", randomSuffix)
	appName := fmt.Sprintf("e2e-application-%d", randomSuffix)
	matchLabels := map[string]string{
		"connects-to": "database",
		"environment": fmt.Sprintf("e2e-%d", randomSuffix),
	}

	t.Logf("Starting end-to-end tests for operator, using suffix '%d'!", randomSuffix)

	resourceRefNamespacedName := types.NamespacedName{Namespace: ns, Name: resourceRef}
	deploymentNamespacedName := types.NamespacedName{Namespace: ns, Name: appName}
	sbrNamespacedName := types.NamespacedName{Namespace: ns, Name: sbrName}
	csvNamespacedName := types.NamespacedName{Namespace: ns, Name: csvName}
	cleanupOpts := cleanupOptions(ctx)

	todoCtx := context.TODO()

	var d appsv1.Deployment
	var sbr *v1alpha1.ServiceBindingRequest

	for _, step := range steps {
		switch step {
		case CSVStep:
			CreateCSV(todoCtx, t, f, cleanupOpts, csvNamespacedName)
		case DBStep:
			CreateDB(todoCtx, t, f, cleanupOpts, resourceRefNamespacedName, secretName)
		case AppStep:
			d = CreateApp(todoCtx, t, f, cleanupOpts, deploymentNamespacedName, matchLabels)
		case SBRStep:
			sbr = CreateSBR(todoCtx, t, f, cleanupOpts, sbrNamespacedName, resourceRef, matchLabels)
		}
	}

	// retrying a few times to identify SBO changes in deployment, this loop is waiting for the
	// operator reconciliation.
	t.Log("Inspecting deployment structure...")
	inspectDeployment(todoCtx, t, f, deploymentNamespacedName, sbrName)

	// retrying a few times to identify SBR status change to "success"
	t.Log("Inspecting SBR status...")
	inspectSBRStatus(todoCtx, t, f, sbrNamespacedName)

	// checking intermediary secret contents, right after deployment the secrets must be in place
	intermediarySecretNamespacedName := types.NamespacedName{Namespace: ns, Name: sbrName}
	sbrSecret, err := assertSBRSecret(todoCtx, f, intermediarySecretNamespacedName)
	require.NoError(t, err, "Intermediary secret contents are invalid: %v", sbrSecret)
	require.NotNil(t, sbrSecret)

	// editing intermediary secret in order to trigger update event
	t.Logf("Updating intermediary secret to have bogus data: '%s'", intermediarySecretNamespacedName)
	updateSBRSecret(todoCtx, t, f, intermediarySecretNamespacedName)

	// retrying a few times to see if secret is back on original state, waiting for operator to
	// reconcile again when detecting the change
	t.Log("Inspecting intermediary secret...")
	inspectSBRSecret(todoCtx, t, f, intermediarySecretNamespacedName)

	// making sure clean up is always executed, and in background
	defer func() {
		t.Log("Cleaning up resource objects...")
		_ = f.Client.Delete(todoCtx, sbr)
		if sbrSecret != nil {
			_ = f.Client.Delete(todoCtx, sbrSecret)
		}
		_ = f.Client.Delete(todoCtx, &d)
	}()
}
