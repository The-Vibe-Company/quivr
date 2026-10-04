# C4 Model Diagrams

The C4 model provides a hierarchical way to visualize software architecture at different levels of abstraction: Context, Containers, Components, and Code.

## C4 Model Levels

1. **System Context** - Shows the system and its users/external systems
2. **Container** - Shows applications, databases, and services within the system
3. **Component** - Shows internal structure of containers
4. **Code** - Class diagrams showing implementation details (use regular class diagrams)

## C4 Context Diagram

Shows the big picture: your system and its relationships with users and external systems.

### Basic Syntax

```mermaid
C4Context
    title System Context for Banking System
    
    Person(customer, "Customer", "A banking customer")
    System(banking, "Banking System", "Allows customers to manage accounts")
    System_Ext(email, "Email System", "Sends emails")
    
    Rel(customer, banking, "Uses")
    Rel(banking, email, "Sends emails via")
```

### Elements

**People:**
```mermaid
C4Context
    Person(user, "User", "Description")
    Person_Ext(external, "External User", "Outside organization")
```

**Systems:**
```mermaid
C4Context
    System(internal, "Internal System", "Description")
    System_Ext(external, "External System", "Description")
    SystemDb(database, "Database System", "Description")
    SystemDb_Ext(external_db, "External DB", "Description")
    SystemQueue(queue, "Message Queue", "Description")
    SystemQueue_Ext(external_queue, "External Queue", "Description")
```

**Relationships:**
```mermaid
C4Context
    Rel(from, to, "Label")
    Rel(from, to, "Label", "Optional Technology")
    BiRel(system1, system2, "Bidirectional")
```

### Comprehensive Context Example

```mermaid
C4Context
    title System Context - E-Commerce Platform
    
    Person(customer, "Customer", "Shops online")
    Person(admin, "Administrator", "Manages products and orders")
    Person_Ext(delivery, "Delivery Personnel", "Delivers orders")
    
    System(ecommerce, "E-Commerce Platform", "Online shopping platform")
    
    System_Ext(payment, "Payment Gateway", "Processes payments")
    System_Ext(email, "Email Service", "Sends notifications")
    System_Ext(sms, "SMS Service", "Sends SMS alerts")
    System_Ext(analytics, "Analytics Platform", "Tracks user behavior")
    SystemQueue_Ext(shipping, "Shipping API", "Calculates shipping rates")
    
    Rel(customer, ecommerce, "Browses products, places orders")
    Rel(admin, ecommerce, "Manages catalog, reviews orders")
    Rel(delivery, ecommerce, "Updates delivery status")
    
    Rel(ecommerce, payment, "Processes payments via", "HTTPS/REST")
    Rel(ecommerce, email, "Sends emails via", "SMTP")
    Rel(ecommerce, sms, "Sends SMS via", "REST API")
    Rel(ecommerce, analytics, "Tracks events", "JavaScript SDK")
    Rel(ecommerce, shipping, "Gets shipping rates", "REST API")
    
    UpdateRelStyle(customer, ecommerce, $offsetX="-50", $offsetY="-30")
    UpdateRelStyle(admin, ecommerce, $offsetX="50", $offsetY="-30")
```

## C4 Container Diagram

Zooms into the system to show containers (applications, databases, services).

### Basic Syntax

```mermaid
C4Container
    title Container Diagram for Banking System
    
    Person(customer, "Customer")
    
    Container_Boundary(banking, "Banking System") {
        Container(web, "Web Application", "React", "Delivers static content")
        Container(api, "API Application", "Node.js", "Provides banking API")
        ContainerDb(db, "Database", "PostgreSQL", "Stores account data")
    }
    
    Rel(customer, web, "Uses", "HTTPS")
    Rel(web, api, "Makes API calls", "HTTPS/JSON")
    Rel(api, db, "Reads/writes", "SQL/TCP")
```

### Container Elements

```mermaid
C4Container
    Container(app, "Application", "Technology", "Description")
    ContainerDb(db, "Database", "PostgreSQL", "Description")
    ContainerQueue(queue, "Queue", "RabbitMQ", "Description")
    Container_Ext(external, "External Service", "Tech", "Description")
```

### Container Boundaries

```mermaid
C4Container
    Container_Boundary(boundary_name, "Boundary Label") {
        Container(app1, "App 1", "Tech")
        Container(app2, "App 2", "Tech")
    }
```

### Comprehensive Container Example

```mermaid
C4Container
    title Container Diagram - E-Commerce Platform
    
    Person(customer, "Customer")
    Person(admin, "Admin")
    System_Ext(payment, "Payment Gateway")
    System_Ext(email, "Email Service")
    
    Container_Boundary(frontend, "Frontend") {
        Container(web, "Web App", "React", "Delivers UI")
        Container(mobile, "Mobile App", "React Native", "Mobile UI")
    }
    
    Container_Boundary(backend, "Backend Services") {
        Container(api, "API Gateway", "Node.js/Express", "Routes requests")
        Container(auth, "Auth Service", "Node.js", "Handles authentication")
        Container(catalog, "Catalog Service", "Python/FastAPI", "Manages products")
        Container(order, "Order Service", "Java/Spring", "Processes orders")
        Container(notification, "Notification Service", "Node.js", "Sends notifications")
    }
    
    Container_Boundary(data, "Data Layer") {
        ContainerDb(postgres, "Main Database", "PostgreSQL", "Stores core data")
        ContainerDb(mongo, "Product DB", "MongoDB", "Product catalog")
        ContainerDb(redis, "Cache", "Redis", "Session & caching")
        ContainerQueue(queue, "Message Queue", "RabbitMQ", "Async processing")
    }
    
    Rel(customer, web, "Uses", "HTTPS")
    Rel(customer, mobile, "Uses", "HTTPS")
    Rel(admin, web, "Manages via", "HTTPS")
    
    Rel(web, api, "Makes calls", "HTTPS/JSON")
    Rel(mobile, api, "Makes calls", "HTTPS/JSON")
    
    Rel(api, auth, "Authenticates", "gRPC")
    Rel(api, catalog, "Gets products", "REST")
    Rel(api, order, "Creates orders", "REST")
    
    Rel(auth, postgres, "Reads/writes users", "SQL")
    Rel(catalog, mongo, "Reads/writes products", "MongoDB Protocol")
    Rel(order, postgres, "Reads/writes orders", "SQL")
    
    Rel(auth, redis, "Stores sessions", "Redis Protocol")
    Rel(api, redis, "Caches data", "Redis Protocol")
    
    Rel(order, queue, "Publishes events", "AMQP")
    Rel(notification, queue, "Consumes events", "AMQP")
    Rel(notification, email, "Sends via", "SMTP")
    Rel(order, payment, "Processes payment", "HTTPS/REST")
```

## C4 Component Diagram

Zooms into a container to show its internal components.

### Basic Syntax

```mermaid
C4Component
    title Component Diagram for API Application
    
    Container(web, "Web App", "React")
    ContainerDb(db, "Database", "PostgreSQL")
    System_Ext(email, "Email System")
    
    Container_Boundary(api, "API Application") {
        Component(controller, "Controller", "Express Router", "Handles HTTP")
        Component(service, "Business Logic", "Service Layer", "Core logic")
        Component(repository, "Data Access", "Repository", "DB operations")
        Component(emailClient, "Email Client", "Client", "Sends emails")
    }
    
    Rel(web, controller, "Makes requests", "HTTPS")
    Rel(controller, service, "Uses")
    Rel(service, repository, "Uses")
    Rel(repository, db, "Reads/writes", "SQL")
    Rel(service, emailClient, "Sends emails via")
    Rel(emailClient, email, "Sends", "SMTP")
```

### Comprehensive Component Example

```mermaid
C4Component
    title Component Diagram - Order Service
    
    Container(api_gateway, "API Gateway", "Node.js")
    ContainerDb(postgres, "Database", "PostgreSQL")
    ContainerQueue(queue, "Message Queue", "RabbitMQ")
    System_Ext(payment, "Payment Gateway")
    System_Ext(inventory, "Inventory Service")
    
    Container_Boundary(order_service, "Order Service") {
        Component(controller, "REST Controllers", "Spring MVC", "HTTP endpoints")
        Component(order_logic, "Order Manager", "Service", "Order processing logic")
        Component(payment_client, "Payment Client", "REST Client", "Payment integration")
        Component(inventory_client, "Inventory Client", "REST Client", "Inventory integration")
        Component(repository, "Order Repository", "JPA", "Database operations")
        Component(event_publisher, "Event Publisher", "Component", "Publishes domain events")
        Component(validator, "Order Validator", "Component", "Validates orders")
    }
    
    Rel(api_gateway, controller, "Routes requests", "HTTPS/REST")
    
    Rel(controller, order_logic, "Delegates to")
    Rel(controller, validator, "Validates with")
    
    Rel(order_logic, payment_client, "Processes payment")
    Rel(order_logic, inventory_client, "Checks stock")
    Rel(order_logic, repository, "Persists orders")
    Rel(order_logic, event_publisher, "Publishes events")
    
    Rel(payment_client, payment, "Calls", "HTTPS/REST")
    Rel(inventory_client, inventory, "Calls", "HTTPS/REST")
    Rel(repository, postgres, "Reads/writes", "JDBC/SQL")
    Rel(event_publisher, queue, "Publishes to", "AMQP")
```

## Microservices Architecture Example

```mermaid
C4Container
    title Microservices Architecture - Streaming Platform
    
    Person(user, "User", "Platform user")
    Person(creator, "Content Creator", "Uploads videos")
    System_Ext(cdn, "CDN", "Delivers media")
    System_Ext(storage, "Object Storage", "Stores videos")
    System_Ext(transcoding, "Transcoding Service", "Processes videos")
    
    Container_Boundary(frontend, "Frontend Layer") {
        Container(web, "Web Application", "Next.js", "Server-rendered UI")
        Container(mobile, "Mobile Apps", "React Native", "iOS/Android apps")
    }
    
    Container_Boundary(api_layer, "API Layer") {
        Container(api_gateway, "API Gateway", "Kong", "Routing, auth, rate limiting")
        Container(graphql, "GraphQL Gateway", "Apollo", "Unified API")
    }
    
    Container_Boundary(services, "Microservices") {
        Container(auth, "Auth Service", "Go", "Authentication & authorization")
        Container(user, "User Service", "Node.js", "User profiles & preferences")
        Container(video, "Video Service", "Python", "Video metadata & management")
        Container(recommendation, "Recommendation Engine", "Python/ML", "Content recommendations")
        Container(analytics, "Analytics Service", "Go", "View tracking & metrics")
        Container(search, "Search Service", "Elasticsearch", "Content search")
        Container(comment, "Comment Service", "Node.js", "Comments & discussions")
    }
    
    Container_Boundary(data, "Data Layer") {
        ContainerDb(user_db, "User Database", "PostgreSQL", "User data")
        ContainerDb(video_db, "Video Database", "MongoDB", "Video metadata")
        ContainerDb(analytics_db, "Analytics DB", "ClickHouse", "Analytics data")
        ContainerDb(cache, "Cache Layer", "Redis Cluster", "Caching & sessions")
        ContainerQueue(event_bus, "Event Bus", "Kafka", "Event streaming")
        ContainerDb(search_index, "Search Index", "Elasticsearch", "Search data")
    }
    
    Rel(user, web, "Uses", "HTTPS")
    Rel(creator, web, "Uploads via", "HTTPS")
    Rel(user, mobile, "Uses", "HTTPS")
    
    Rel(web, api_gateway, "API calls", "HTTPS/REST")
    Rel(mobile, api_gateway, "API calls", "HTTPS/REST")
    Rel(web, graphql, "Queries", "HTTPS/GraphQL")
    
    Rel(api_gateway, auth, "Authenticates", "gRPC")
    Rel(graphql, video, "Gets videos", "gRPC")
    Rel(graphql, user, "Gets users", "gRPC")
    Rel(graphql, recommendation, "Gets recommendations", "gRPC")
    
    Rel(video, storage, "Stores videos", "S3 API")
    Rel(video, transcoding, "Sends for transcoding", "REST")
    Rel(video, cdn, "Publishes to", "API")
    
    Rel(auth, user_db, "Manages credentials", "SQL")
    Rel(user, user_db, "Stores profiles", "SQL")
    Rel(video, video_db, "Stores metadata", "MongoDB")
    Rel(analytics, analytics_db, "Stores metrics", "SQL")
    
    Rel(auth, cache, "Sessions", "Redis")
    Rel(video, cache, "Caches metadata", "Redis")
    Rel(search, search_index, "Indexes & queries", "REST")
    
    Rel(video, event_bus, "Publishes VideoUploaded", "Kafka")
    Rel(analytics, event_bus, "Publishes ViewStarted", "Kafka")
    Rel(recommendation, event_bus, "Consumes events", "Kafka")
    Rel(search, event_bus, "Consumes events", "Kafka")
```

## Best Practices

1. **Use appropriate level** - Context for stakeholders, Container for architects, Component for developers
2. **Keep it focused** - One system per Context diagram, one container per Component diagram
3. **Show key relationships** - Don't clutter with every possible connection
4. **Use consistent naming** - Same names across all diagram levels
5. **Add technology details** - Specify frameworks, languages, protocols at Container/Component level
6. **Update regularly** - Keep diagrams in sync with architecture
7. **Use boundaries** - Group related containers/components logically
8. **Document protocols** - Show communication methods (REST, gRPC, messaging)
9. **Highlight external systems** - Use *_Ext variants for clarity
10. **Start simple** - Begin with Context, drill down as needed

## Common Architecture Patterns

### Monolithic Application
```mermaid
C4Container
    Person(user, "User")
    
    Container_Boundary(system, "Application") {
        Container(app, "Web Application", "Ruby on Rails", "Monolithic application")
        ContainerDb(db, "Database", "PostgreSQL", "Application database")
        ContainerDb(cache, "Cache", "Redis", "Session store")
    }
    
    Rel(user, app, "Uses", "HTTPS")
    Rel(app, db, "Reads/writes", "SQL")
    Rel(app, cache, "Caches", "Redis Protocol")
```

### Three-Tier Architecture
```mermaid
C4Container
    Person(user, "User")
    
    Container_Boundary(presentation, "Presentation Tier") {
        Container(web, "Web Server", "Nginx", "Static content")
        Container(app, "App Server", "Node.js", "Application logic")
    }
    
    Container_Boundary(business, "Business Tier") {
        Container(api, "API Server", "Java", "Business logic")
    }
    
    Container_Boundary(data, "Data Tier") {
        ContainerDb(db, "Database", "MySQL", "Data storage")
    }
    
    Rel(user, web, "Uses", "HTTPS")
    Rel(web, app, "Proxies to", "HTTP")
    Rel(app, api, "Calls", "REST")
    Rel(api, db, "Reads/writes", "SQL")
```

### Event-Driven Architecture
```mermaid
C4Container
    Person(user, "User")
    
    Container(frontend, "Frontend", "React", "User interface")
    Container(api, "API Gateway", "Kong", "API routing")
    
    Container_Boundary(services, "Services") {
        Container(order, "Order Service", "Java", "Order processing")
        Container(inventory, "Inventory Service", "Go", "Stock management")
        Container(notification, "Notification Service", "Node.js", "Alerts")
    }
    
    ContainerQueue(events, "Event Bus", "Kafka", "Event streaming")
    ContainerDb(db, "Databases", "Various", "Service databases")
    
    Rel(user, frontend, "Uses")
    Rel(frontend, api, "Calls")
    Rel(api, order, "Routes to")
    
    Rel(order, events, "Publishes OrderCreated")
    Rel(events, inventory, "Consumes events")
    Rel(events, notification, "Consumes events")
    
    Rel(order, db, "Persists")
    Rel(inventory, db, "Persists")
```
